// Package observer captures MQTT traffic and optionally serves model-specific replies.
package observer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"database/sql"
	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/output"
	"github.com/danielskowronski/qiotlctl/internal/storage"
	"github.com/danielskowronski/qiotlctl/internal/transport"
	paho "github.com/eclipse/paho.mqtt.golang"
)

type Stats struct {
	Diagnostics    *diagnostic.Registry
	Outputs        output.Stats
	Connected      atomic.Bool
	Subscribed     atomic.Bool
	StorageOK      atomic.Bool
	Received       atomic.Uint64
	Decoded        atomic.Uint64
	DecodeErrors   atomic.Uint64
	Samples        atomic.Uint64
	Readings       atomic.Uint64
	Recorded       atomic.Uint64
	QueueOverflows atomic.Uint64
	Reconnects     atomic.Uint64
	QueueDepth     atomic.Int64
	Replies        atomic.Uint64
	ReplyErrors    atomic.Uint64
	ClockAnomalies atomic.Uint64
}

type Route struct {
	Device    config.Device
	Direction domain.Direction
}

func Routes(c config.Config) map[string]Route {
	r := map[string]Route{}
	for _, d := range c.Devices {
		if !d.Active() {
			continue
		}
		r[d.Topics.Telemetry] = Route{d, domain.FromDevice}
		if c.Runtime.ObserveCommands && d.Topics.Commands != "" {
			r[d.Topics.Commands] = Route{d, domain.ToDevice}
		}
	}
	return r
}

func DecodeCapture(routes map[string]Route, c domain.Capture) domain.Event {
	r, ok := routes[c.Topic]
	if !ok {
		return domain.Event{SchemaVersion: 1, RecordID: c.ID, ReceivedAt: c.ReceivedAt, Topic: c.Topic, Direction: c.Direction, Error: "topic is not mapped to an enabled device"}
	}
	if c.Direction.Valid() && c.Direction != r.Direction {
		return domain.Event{SchemaVersion: 1, RecordID: c.ID, ReceivedAt: c.ReceivedAt, Topic: c.Topic, Direction: c.Direction, Error: "stored direction conflicts with configured topic"}
	}
	if c.Payload == nil && c.StoredEvent != nil {
		e := *c.StoredEvent
		e.RecordID = c.ID
		return e
	}
	e, err := device.Decode(c.Payload, r.Device.Protocol, r.Device.Model, r.Direction, c.ReceivedAt)
	if err != nil {
		e = domain.Event{SchemaVersion: 1, ReceivedAt: c.ReceivedAt, Direction: r.Direction, Protocol: r.Device.Protocol, Error: err.Error()}
	}
	e.DeviceID = r.Device.ID
	e.Alias = r.Device.Alias
	if e.Model == "" {
		e.Model = r.Device.Model
	}
	e.Topic = c.Topic
	e.RecordID = c.ID
	return e
}

type Emit func(domain.Event) error

func Run(ctx context.Context, c config.Config, stats *Stats, emit Emit) error {
	if err := c.Validate(true); err != nil {
		return err
	}
	if stats == nil {
		stats = &Stats{}
	}
	if stats.Diagnostics == nil {
		stats.InitHealth(c)
	}
	var recorder *storage.Recorder
	if c.Storage.Path != "" {
		var err error
		recorder, err = storage.OpenRecorder(c.Storage.Path, c.Storage.CaptureRaw)
		if err != nil {
			stats.Diagnostics.Set("storage", diagnostic.Failed, "cannot open capture database", "check storage path, permissions and database integrity", nil)
			return err
		}
		recorder.MaxBytes = c.Storage.MaxBytes
		defer recorder.Close()
	}
	stats.StorageOK.Store(true)
	stats.Diagnostics.Set("storage", diagnostic.OK, "capture storage initialized (or recording disabled)", "", nil)
	var outputs *output.Store
	if recorder != nil && (c.Outputs.VictoriaMetrics.Enabled || c.Outputs.HomeAssistant.Enabled) {
		outputs = output.NewStore(recorder, c)
		stop, err := output.Start(ctx, outputs, &stats.Outputs, stats.CaptureReady)
		if err != nil {
			return err
		}
		defer stop()
	}
	routes := Routes(c)
	queue := make(chan domain.Capture, c.Runtime.QueueSize)
	fail := make(chan error, 1)
	reportFailure := func(err error) {
		select {
		case fail <- err:
		default:
		}
	}
	options, err := transport.Options(c.MQTT)
	if err != nil {
		return err
	}
	options.SetDefaultPublishHandler(func(_ paho.Client, m paho.Message) {
		stats.Received.Add(1)
		qos, retained, duplicate := m.Qos(), m.Retained(), m.Duplicate()
		capture := domain.Capture{Topic: m.Topic(), Payload: append([]byte(nil), m.Payload()...), ReceivedAt: time.Now().UTC(), QoS: &qos, Retained: &retained, Duplicate: &duplicate}
		if r, ok := routes[capture.Topic]; ok {
			capture.Direction = r.Direction
		}
		select {
		case <-ctx.Done():
		case queue <- capture:
			stats.QueueDepth.Store(int64(len(queue)))
		default:
			stats.QueueOverflows.Add(1)
			stats.Subscribed.Store(false)
			reportFailure(errors.New("capture queue overflow; observer stopped rather than silently dropping data"))
		}
	})
	options.SetConnectRetry(false).SetAutoReconnect(false)
	options.SetConnectionLostHandler(func(_ paho.Client, err error) {
		stats.Connected.Store(false)
		stats.Subscribed.Store(false)
		stats.Diagnostics.Set("mqtt.connection", diagnostic.Failed, "MQTT connection lost: "+transport.NetworkReason(err), "check broker connectivity; reconnecting automatically", nil)
		stats.Diagnostics.Set("mqtt.subscriptions", diagnostic.Unknown, "waiting for reconnection", "", nil)
	})
	client := paho.NewClient(options)
	connectionCtx, stopConnection := context.WithCancel(ctx)
	connectionDone := make(chan struct{})
	go func() { defer close(connectionDone); maintainConnection(connectionCtx, client, c, routes, stats) }()
	defer func() { stopConnection(); <-connectionDone }()
	responder := responder{}
	process := func(capture domain.Capture) error {
		stats.QueueDepth.Store(int64(len(queue)))
		e := DecodeCapture(routes, capture)
		if e.Error != "" {
			stats.DecodeErrors.Add(1)
		} else {
			stats.Decoded.Add(1)
		}
		stats.Samples.Add(uint64(len(e.Samples)))
		for _, s := range e.Samples {
			stats.Readings.Add(uint64(len(s.Readings)))
			if s.TimeQuality == "before-2020" || s.TimeQuality == "future" {
				stats.ClockAnomalies.Add(1)
			}
		}
		if recorder != nil {
			// Flush remains bounded even after the caller's context is canceled.
			writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			var hooks []func(*sql.Tx) error
			if outputs != nil {
				hooks = append(hooks, outputs.CaptureHook(writeCtx, e))
			}
			id, err := recorder.Record(writeCtx, capture, e, hooks...)
			cancel()
			if err != nil {
				stats.StorageOK.Store(false)
				stats.Diagnostics.Set("storage", diagnostic.Failed, "capture commit failed", "check disk capacity, permissions and database health", nil)
				return fmt.Errorf("record capture: %w", err)
			}
			e.RecordID = id
			stats.Recorded.Add(1)
		}
		var outgoing *domain.Event
		if route, ok := routes[capture.Topic]; ok {
			var err error
			outgoing, err = responder.reply(ctx, c, route.Device, capture, e, client, recorder, stats)
			if err != nil {
				stats.StorageOK.Store(false)
				return err
			}
		}
		if emit != nil {
			if err := emit(e); err != nil {
				return err
			}
			if outgoing != nil {
				return emit(*outgoing)
			}
		}
		return nil
	}
	for {
		select {
		case err := <-fail:
			return err
		case capture := <-queue:
			if err = process(capture); err != nil {
				return err
			}
		case <-ctx.Done():
			client.Disconnect(1000)
			deadline := time.Now().Add(5 * time.Second)
			for len(queue) > 0 && time.Now().Before(deadline) {
				if err = process(<-queue); err != nil {
					return err
				}
			}
			if len(queue) > 0 {
				return errors.New("shutdown deadline exceeded with pending captures")
			}
			return nil
		}
	}
}

func DeviceList(c config.Config) []config.Device {
	keys := make([]string, 0, len(c.Devices))
	for k := range c.Devices {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]config.Device, 0, len(keys))
	for _, k := range keys {
		result = append(result, c.Devices[k])
	}
	return result
}

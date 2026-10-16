package command

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/observer"
	"github.com/danielskowronski/qiotlctl/internal/protocol"
	"github.com/danielskowronski/qiotlctl/internal/storage"
	"github.com/danielskowronski/qiotlctl/internal/transport"
	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
)

type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string { return e.Message }
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 1
}

type Options struct {
	lockHeld            bool
	Timeout             time.Duration
	WaitForUplink       bool
	RequireConfirmation bool
}

var localLocks sync.Map // keyed by broker and exact device uplink; process-local only

func Execute(parent context.Context, c config.Config, d config.Device, req Request, opt Options) (result domain.CommandResult, resultErr error) {
	result = domain.CommandResult{SchemaVersion: 1, OperationID: uuid.NewString(), DeviceID: d.ID, Alias: d.Alias, Operation: req.Operation, Status: "failed", Evidence: "none", StartedAt: time.Now().UTC()}
	defer func() {
		now := time.Now().UTC()
		if result.FinishedAt == nil {
			result.FinishedAt = &now
		}
		if resultErr != nil {
			result.Error = resultErr.Error()
			if result.AttemptedAt == nil && result.Status == "prepared" {
				result.Status = "failed"
			}
		}
	}()
	sessionConfig := c
	// Exporters are daemon concerns; --record '' must work with an exporter-enabled config.
	sessionConfig.Outputs.HomeAssistant.Enabled = false
	sessionConfig.Outputs.VictoriaMetrics.Enabled = false
	sessionDevice := d
	disabled := false
	sessionDevice.Responses.Enabled = &disabled
	sessionConfig.Devices = map[string]config.Device{d.Alias: sessionDevice}
	if err := sessionConfig.Validate(true); err != nil {
		return result, &Error{2, err.Error()}
	}
	plan, err := Build(d, req, time.Now().UTC())
	if err != nil {
		code := 2
		if errors.Is(err, ErrUnsupported) {
			code = 3
		}
		return result, &Error{code, err.Error()}
	}
	if opt.Timeout == 0 {
		opt.Timeout = c.Commands.Timeout.Duration()
	}
	if opt.Timeout <= 0 {
		return result, &Error{2, "command timeout must be positive"}
	}
	mutex, _ := localLocks.LoadOrStore(c.MQTT.URL+"\x00"+d.Topics.Telemetry, &sync.Mutex{})
	lock := mutex.(*sync.Mutex)
	if !opt.lockHeld && !lock.TryLock() {
		return result, &Error{8, "another operation for this device is active in this process"}
	}
	if !opt.lockHeld {
		defer lock.Unlock()
	}
	ctx, cancel := context.WithTimeout(parent, opt.Timeout)
	defer cancel()
	var recorder *storage.Recorder
	if c.Storage.Path != "" {
		recorder, err = storage.OpenRecorder(c.Storage.Path, c.Storage.CaptureRaw)
		if err != nil {
			return result, fmt.Errorf("open command journal: %w", err)
		}
		recorder.MaxBytes = c.Storage.MaxBytes
		defer recorder.Close()
		// Register after Close so the final transition is persisted before closing.
		defer func() {
			now := time.Now().UTC()
			result.FinishedAt = &now
			if resultErr != nil {
				result.Error = resultErr.Error()
				if result.AttemptedAt == nil && result.Status == "prepared" {
					result.Status = "failed"
				}
			}
			writeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			if e := recorder.RecordCommand(writeCtx, result); e != nil {
				resultErr = fmt.Errorf("record final command outcome (outcome above may still hold): %w", e)
				result.Error = resultErr.Error()
			}
		}()
	}
	saveState := func() error {
		if recorder == nil {
			return nil
		}
		return recorder.RecordCommand(ctx, result)
	}
	routes := map[string]observer.Route{d.Topics.Telemetry: {Device: d, Direction: domain.FromDevice}, d.Topics.Commands: {Device: d, Direction: domain.ToDevice}}
	capture := func(cap domain.Capture) (domain.Event, error) {
		e := observer.DecodeCapture(routes, cap)
		if recorder != nil {
			id, err := recorder.Record(ctx, cap, e)
			if err != nil {
				return e, fmt.Errorf("record command traffic: %w", err)
			}
			e.RecordID = id
		}
		return e, nil
	}
	queue := make(chan domain.Capture, c.Runtime.QueueSize)
	failure := make(chan error, 1)
	fail := func(e error) {
		select {
		case failure <- e:
		default:
		}
	}
	options, err := transport.Options(c.MQTT)
	if err != nil {
		return result, &Error{4, err.Error()}
	}
	// One-shot sessions never share the daemon client ID and never replay a
	// possibly executed request after reconnect. A new invocation is explicit.
	options.SetClientID("qiotl-cmd-" + result.OperationID[:12]).SetAutoReconnect(false).SetConnectRetry(false)
	options.SetConnectionLostHandler(func(_ paho.Client, _ error) { fail(errors.New("MQTT connection lost; command will not be retried")) })
	options.SetDefaultPublishHandler(func(_ paho.Client, m paho.Message) {
		if m.Topic() != d.Topics.Telemetry {
			return
		}
		qos, retained, duplicate := m.Qos(), m.Retained(), m.Duplicate()
		cap := domain.Capture{Topic: m.Topic(), Payload: append([]byte(nil), m.Payload()...), ReceivedAt: time.Now().UTC(), Direction: domain.FromDevice, QoS: &qos, Retained: &retained, Duplicate: &duplicate}
		select {
		case <-ctx.Done():
		case queue <- cap:
		default:
			fail(errors.New("command receive queue overflow; result is uncertain"))
		}
	})
	client := paho.NewClient(options)
	defer client.Disconnect(250)
	wait := func(token paho.Token, operation string) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-failure:
			return e
		case <-token.Done():
			return transport.Wait(ctx, token, operation)
		}
	}
	failResult := func(e error) (domain.CommandResult, error) {
		code := 4
		if errors.Is(e, context.DeadlineExceeded) {
			code = 5
			result.Status = "timed_out"
		}
		if errors.Is(e, context.Canceled) {
			code = 130
			result.Status = "cancelled"
		}
		if result.AttemptedAt != nil {
			result.Status = "unknown"
			result.Evidence = "publication attempted; device outcome is unknown"
		}
		return result, &Error{code, e.Error()}
	}
	if err = wait(client.Connect(), "connection"); err != nil {
		return failResult(err)
	}
	if err = wait(client.Subscribe(d.Topics.Telemetry, c.MQTT.QoS, nil), "subscription"); err != nil {
		return failResult(err)
	}
	if opt.WaitForUplink {
		ready := false
		for !ready {
			select {
			case <-ctx.Done():
				return failResult(ctx.Err())
			case err = <-failure:
				return failResult(err)
			case cap := <-queue:
				event, e := capture(cap)
				if e != nil {
					return result, e
				}
				msg, e := protocol.Decode(cap.Payload, plan.Protocol)
				ready = e == nil && event.Error == "" && !*cap.Retained && !*cap.Duplicate && device.IdentityMatches(msg, d.MAC)
			}
		}
	}
	// Build timestamp-bearing payloads immediately before sending, after any wake wait.
	plan, err = Build(d, req, time.Now().UTC())
	if err != nil {
		return result, err
	}
	result.Detail = plan.Detail
	result.RequestID = plan.RequestID
	result.PayloadSHA256 = plan.PayloadSHA256
	result.PayloadSize = plan.PayloadSize
	matcher := Matcher{Plan: plan, Device: d}
	result.Status = "prepared"
	if err = saveState(); err != nil {
		return result, fmt.Errorf("record command intent: %w", err)
	}
	// Persist an explicitly labelled intent before the side effect, preserving crash ambiguity.
	out := domain.Capture{Topic: plan.Topic, Payload: plan.Payload, Direction: domain.ToDevice, ReceivedAt: time.Now().UTC()}
	event := plan.Preview
	if event.Metadata == nil {
		event.Metadata = map[string]any{}
	}
	event.Metadata["operation_id"] = result.OperationID
	event.Metadata["transmission"] = "intent; consult command_events for outcome"
	if recorder != nil {
		if _, err = recorder.Record(ctx, out, event); err != nil {
			return result, fmt.Errorf("record outbound intent: %w", err)
		}
	}
	select {
	case <-ctx.Done():
		return failResult(ctx.Err())
	case err = <-failure:
		return failResult(err)
	default:
	}
	if !client.IsConnectionOpen() {
		return failResult(errors.New("MQTT disconnected before publication"))
	}
	now := time.Now().UTC()
	matcher.SentAt = now
	result.AttemptedAt = &now
	result.Status = "attempting"
	if err = saveState(); err != nil {
		result.AttemptedAt = nil
		result.Status = "failed"
		return result, fmt.Errorf("record publish attempt: %w", err)
	}
	matcher.SentAt = time.Now().UTC()
	if err = transport.Publish(ctx, client, plan.Topic, c.MQTT.QoS, plan.Payload); err != nil {
		return failResult(err)
	}
	published := time.Now().UTC()
	result.PublishedAt = &published
	result.Status = "published"
	result.Evidence = "MQTT publish completed; device execution unproven"
	if err = saveState(); err != nil {
		return result, fmt.Errorf("record publication: %w", err)
	}
	if plan.Completion == "published" {
		if opt.RequireConfirmation {
			return result, &Error{7, "publication succeeded but operation has no device confirmation"}
		}
		return result, nil
	}
	for {
		select {
		case <-ctx.Done():
			return failResult(ctx.Err())
		case err = <-failure:
			return failResult(err)
		case cap := <-queue:
			event, e := capture(cap)
			if e != nil {
				result.Status = "unknown"
				return result, e
			}
			status, evidence := matcher.Match(cap, event)
			if status == "" {
				continue
			}
			result.Status = status
			result.Evidence = evidence
			result.Response = &event
			if status == "rejected" {
				return result, &Error{6, evidence}
			}
			return result, nil
		}
	}
}

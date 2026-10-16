package output

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/transport"
	paho "github.com/eclipse/paho.mqtt.golang"
)

type MQTTPublisher struct{ Client paho.Client }

func (p MQTTPublisher) Publish(ctx context.Context, topic string, qos byte, retain bool, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Paho's token reports broker acceptance, not consumption by Home Assistant.
	done := make(chan error, 1)
	go func() { done <- transport.Wait(ctx, p.Client.Publish(topic, qos, retain, b), "HA publication") }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}
func Start(parent context.Context, s *Store, stats *Stats, sourceReady func() bool) (func(), error) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	stats.VMHealthy.Store(false)
	stats.HAHealthy.Store(false)
	if s.Config.Outputs.VictoriaMetrics.Enabled {
		sender, err := NewVMSender(s.Config.Outputs.VictoriaMetrics)
		if err != nil {
			cancel()
			return nil, err
		}
		wg.Add(1)
		go func() { defer wg.Done(); defer sender.Close(); (&VMWorker{s, sender, stats}).Run(ctx) }()
	}
	if s.Config.Outputs.HomeAssistant.Enabled {
		// Validate TLS options before launching workers, including certificate files.
		if _, err := transport.Options(s.Config.MQTT); err != nil {
			cancel()
			wg.Wait()
			return nil, err
		}
		wg.Add(1)
		go func() { defer wg.Done(); runHA(ctx, s, stats, sourceReady) }()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			_ = s.UpdateStats(ctx, stats)
			if !Wait(ctx, 5*time.Second) {
				return
			}
		}
	}()
	return func() { cancel(); wg.Wait() }, nil
}
func runHA(ctx context.Context, s *Store, stats *Stats, sourceReady func() bool) {
	h := s.Config.Outputs.HomeAssistant
	r := stats.Diagnostics
	for ctx.Err() == nil {
		options, err := transport.Options(s.Config.MQTT)
		if err != nil {
			stats.HAHealthy.Store(false)
			return
		}
		options.SetClientID(options.ClientID+"-ha").SetConnectRetry(false).SetWill(BridgeTopic(h), "offline", 1, true)
		var refresh atomic.Bool
		var subscribed atomic.Bool
		options.SetOnConnectHandler(func(client paho.Client) { subscribed.Store(false); refresh.Store(true) })

		client := paho.NewClient(options)
		connectCtx, stopConnect := context.WithTimeout(ctx, s.Config.MQTT.ConnectTimeout.Duration())
		err = transport.InspectToken(connectCtx, client.Connect(), "HA connection")
		stopConnect()
		if err != nil {
			r.Set("ha.connection", diagnostic.Failed, err.Error(), "check broker credentials/connectivity and permission for the bridge last will", nil)
			s.DeliveryStatus(ctx, HATarget(s.Config), err)
			stats.HAHealthy.Store(false)
			stats.HAErrors.Add(1)
			client.Disconnect(100)
			if !Wait(ctx, 5*time.Second) {
				return
			}
			continue
		}
		publisher := MQTTPublisher{client}
		r.Set("ha.connection", diagnostic.OK, "HA exporter connected", "", nil)
		worker := HAWorker{Store: s, Stats: stats, Publisher: publisher}
		var inspected time.Time
		discoveryOK := false
		bridge := ""
		retryState := false
		for ctx.Err() == nil {
			if !client.IsConnectionOpen() {
				r.Set("ha.connection", diagnostic.Failed, "HA exporter disconnected", "check broker connectivity; reconnecting automatically", nil)
				stats.HAHealthy.Store(false)
				bridge = ""
				refresh.Store(true)
				if !Wait(ctx, time.Second) {
					break
				}
				continue
			}
			if !subscribed.Load() {
				r.Set("ha.connection", diagnostic.OK, "HA exporter connected", "", nil)
				subctx, stop := context.WithTimeout(ctx, 5*time.Second)
				suberr := transport.InspectToken(subctx, client.Subscribe(h.BirthTopic, 1, func(_ paho.Client, m paho.Message) {
					if string(m.Payload()) == "online" {
						refresh.Store(true)
					}
				}), "HA birth subscription")
				stop()
				if suberr != nil {
					r.Set("ha.subscription", diagnostic.Failed, suberr.Error(), "allow subscribe for "+h.BirthTopic, nil)
					stats.HAHealthy.Store(false)
					stats.HAErrors.Add(1)
					s.DeliveryStatus(ctx, HATarget(s.Config), suberr)
					if !Wait(ctx, 5*time.Second) {
						break
					}
					continue
				}
				subscribed.Store(true)
				r.Set("ha.subscription", diagnostic.OK, "birth subscription accepted: "+h.BirthTopic, "", nil)
			}
			full := refresh.Swap(false)
			if full {
				bridge = ""
				inspected = time.Time{}
			}
			available := "offline"
			if sourceReady() {
				available = "online"
			}
			if available != bridge {
				err = worker.publish(ctx, BridgeTopic(h), true, []byte(available))
				if err == nil {
					bridge = available
				}
			}
			if err == nil {
				err = worker.Sync(ctx, time.Now(), full)
			}
			// Discovery may be processed after the first state publication. Re-send
			// state once on the next tick, without resetting its measurement freshness.
			if retryState {
				worker.sent = nil
				retryState = false
			}
			if full {
				retryState = true
			}
			if err == nil {
				r.Set("ha.publication", diagnostic.OK, "HA synchronization completed; retained discovery is verified separately", "", nil)
				if time.Since(inspected) >= s.Config.Health.CheckInterval.Duration() {
					states, stateErr := s.HAStates(ctx)
					if stateErr != nil {
						discoveryOK = false
						r.Set("ha.discovery", diagnostic.Failed, "cannot inspect HA state database", "check capture database health", nil)
					} else {
						check, stop := context.WithTimeout(ctx, s.Config.Health.CheckTimeout.Duration())
						discoveryOK = InspectDiscovery(check, s.Config, ExpectedDiscovery(s.Config, states), r)
						stop()
					}
					inspected = time.Now()
				}
			} else {
				r.Set("ha.publication", diagnostic.Failed, "HA synchronization failed", "check publish ACLs for discovery/state/availability, connection and storage", nil)
			}
			stats.HAHealthy.Store(err == nil && discoveryOK)
			if full || err != nil {
				var safeErr error
				if err != nil {
					safeErr = errors.New("HA synchronization failed; inspect daemon health")
				}
				s.DeliveryStatus(ctx, HATarget(s.Config), safeErr)
			}
			if err != nil {
				stats.HAErrors.Add(1)
				refresh.Store(true)
				err = nil
			}
			if !Wait(ctx, time.Second) {
				break
			}
		}
		if client.IsConnectionOpen() {
			shutdown, stop := context.WithTimeout(context.Background(), time.Second)
			_ = publisher.Publish(shutdown, BridgeTopic(h), 1, true, []byte("offline"))
			stop()
		}
		client.Disconnect(100)
	}
}

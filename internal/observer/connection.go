package observer

import (
	"context"
	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/output"
	"github.com/danielskowronski/qiotlctl/internal/transport"
	paho "github.com/eclipse/paho.mqtt.golang"
	"time"
)

// maintainConnection owns connect/subscribe attempts. Recoverable ACL and transport
// failures remain observable without turning the daemon into a restart loop.
func maintainConnection(ctx context.Context, client paho.Client, c config.Config, routes map[string]Route, s *Stats) {
	defer func() { s.Connected.Store(false); s.Subscribed.Store(false); client.Disconnect(100) }()
	filters := map[string]byte{}
	for topic := range routes {
		filters[topic] = c.MQTT.QoS
	}
	backoff := time.Second
	for ctx.Err() == nil {
		if !client.IsConnectionOpen() {
			s.Connected.Store(false)
			s.Subscribed.Store(false)
			attempt, stop := context.WithTimeout(ctx, c.MQTT.ConnectTimeout.Duration())
			err := transport.InspectToken(attempt, client.Connect(), "MQTT connect")
			stop()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				s.Diagnostics.Set("mqtt.connection", diagnostic.Failed, err.Error(), "check broker address, TLS and MQTT credentials", nil)
				s.Diagnostics.Set("mqtt.subscriptions", diagnostic.Unknown, "waiting for MQTT connection", "", nil)
				client.Disconnect(100)
				if !output.Wait(ctx, backoff) {
					return
				}
				backoff = min(backoff*2, c.MQTT.ReconnectMax.Duration())
				continue
			}
			s.Connected.Store(true)
			s.Reconnects.Add(1)
			s.Diagnostics.Set("mqtt.connection", diagnostic.OK, "connected to configured broker", "", nil)
		}
		if !s.Subscribed.Load() {
			attempt, stop := context.WithTimeout(ctx, c.MQTT.ConnectTimeout.Duration())
			err := transport.InspectToken(attempt, client.SubscribeMultiple(filters, nil), "MQTT subscriptions")
			stop()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				s.Diagnostics.Set("mqtt.subscriptions", diagnostic.Failed, err.Error(), "allow subscribe for the listed device topics", nil)
				if !output.Wait(ctx, backoff) {
					return
				}
				backoff = min(backoff*2, c.MQTT.ReconnectMax.Duration())
				continue
			}
			s.Subscribed.Store(true)
			backoff = time.Second
			s.Diagnostics.Set("mqtt.subscriptions", diagnostic.OK, "all capture subscriptions accepted", "", nil)
		}
		if !output.Wait(ctx, 250*time.Millisecond) {
			return
		}
	}
}

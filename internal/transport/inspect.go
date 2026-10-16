package transport

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
)

type Observed struct {
	Payload  []byte
	Retained bool
}

// Inspect uses an independent client without a will. A nonempty probe is an
// explicitly requested, nonretained publication on a caller-validated topic.
func Inspect(ctx context.Context, c config.MQTT, topics []string, probe string) (map[string]Observed, error) {
	c.ClientID = "qiotl-doctor-" + uuid.NewString()
	o, err := Options(c)
	if err != nil {
		return nil, errors.New("invalid MQTT TLS configuration")
	}
	o.SetConnectRetry(false).SetAutoReconnect(false)
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, ctx.Err()
		}
		o.SetConnectTimeout(min(c.ConnectTimeout.Duration(), remaining))
	}
	lost := make(chan struct{}, 1)
	o.SetConnectionLostHandler(func(_ paho.Client, _ error) {
		select {
		case lost <- struct{}{}:
		default:
		}
	})
	var mu sync.Mutex
	messages := map[string]Observed{}
	filters := map[string]byte{}
	for _, topic := range topics {
		filters[topic] = 1
	}
	if probe != "" {
		filters[probe] = 1
	}
	o.SetDefaultPublishHandler(func(_ paho.Client, m paho.Message) {
		if _, ok := filters[m.Topic()]; !ok {
			return
		}
		if len(m.Payload()) > 64<<10 {
			return
		}
		mu.Lock()
		// Keep retained readback evidence when a simultaneous HA birth causes
		// an additional nonretained delivery to this already-subscribed client.
		if previous, ok := messages[m.Topic()]; !ok || !previous.Retained || m.Retained() {
			messages[m.Topic()] = Observed{append([]byte(nil), m.Payload()...), m.Retained()}
		}
		mu.Unlock()
	})
	client := paho.NewClient(o)
	defer client.Disconnect(100)
	if err = InspectToken(ctx, client.Connect(), "MQTT connect"); err != nil {
		return nil, err
	}
	if len(filters) == 0 {
		return messages, nil
	}
	if err = InspectToken(ctx, client.SubscribeMultiple(filters, nil), "MQTT inspection"); err != nil {
		return nil, err
	}
	nonce := uuid.NewString()
	if probe != "" {
		if err = InspectToken(ctx, client.Publish(probe, 1, false, nonce), "diagnostic publish"); err != nil {
			return nil, err
		}
	}
	// Retained replies may follow SUBACK. Wait within the caller's deadline;
	// nonretained telemetry/birth is never required to arrive on every inspection.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-lost:
			return nil, errors.New("MQTT connection lost before inspection completed")
		case <-ctx.Done():
			mu.Lock()
			defer mu.Unlock()
			if probe != "" {
				m, ok := messages[probe]
				if !ok || m.Retained || string(m.Payload) != nonce {
					return nil, errors.New("diagnostic publication not observed; publish or subscribe may be filtered")
				}
			}
			copy := make(map[string]Observed, len(messages))
			for k, v := range messages {
				copy[k] = v
			}
			return copy, nil
		case <-ticker.C:
			mu.Lock()
			complete := len(messages) == len(filters)
			if probe != "" {
				m := messages[probe]
				complete = complete && !m.Retained && string(m.Payload) == nonce
			}
			if complete {
				copy := make(map[string]Observed, len(messages))
				for k, v := range messages {
					copy[k] = v
				}
				mu.Unlock()
				return copy, nil
			}
			mu.Unlock()
		}
	}
}

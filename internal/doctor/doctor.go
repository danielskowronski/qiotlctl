// Package doctor performs bounded diagnostics without starting a recorder/exporter.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/output"
	"github.com/danielskowronski/qiotlctl/internal/transport"
	"github.com/google/uuid"
)

func Run(ctx context.Context, c config.Config, probePublish bool) diagnostic.Snapshot {
	r := diagnostic.New()
	for _, id := range []string{"config", "mqtt.connection", "mqtt.subscriptions"} {
		r.Register(id, true, 0, 0)
	}
	for _, group := range []struct {
		enabled bool
		ids     []string
	}{
		{c.Outputs.HomeAssistant.Enabled, []string{"ha.discovery", "ha.subscription"}},
		{c.Outputs.VictoriaMetrics.Enabled, []string{"vm.endpoint", "vm.backlog", "vm.delivery"}},
	} {
		for _, id := range group.ids {
			r.Register(id, group.enabled, 0, 0)
			if !group.enabled {
				r.Set(id, diagnostic.Disabled, "output disabled", "", nil)
			}
		}
	}
	if err := c.Validate(true); err != nil {
		r.Set("config", diagnostic.Failed, err.Error(), "correct configuration or environment overrides", nil)
		return r.Snapshot(time.Now())
	}
	r.Set("config", diagnostic.OK, "effective configuration validated", "", nil)
	r.Register("storage", c.Storage.Path != "", 0, 0)
	states := inspectStorage(ctx, c, r)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); inspectMQTT(ctx, c, probePublish, r) }()
	if c.Outputs.HomeAssistant.Enabled {
		wg.Add(1)
		go func() { defer wg.Done(); inspectHA(ctx, c, output.ExpectedDiscovery(c, states), r) }()
	}
	if c.Outputs.VictoriaMetrics.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sender, err := output.NewVMSender(c.Outputs.VictoriaMetrics)
			if err == nil {
				defer sender.Close()
				check, stop := context.WithTimeout(ctx, c.Health.CheckTimeout.Duration())
				err = sender.Send(check, nil)
				stop()
			}
			if err != nil {
				r.Set("vm.endpoint", diagnostic.Failed, err.Error(), "check import URL, VMUser credentials, TLS and routing; zero-sample probe may be unsupported", nil)
			} else {
				r.Set("vm.endpoint", diagnostic.OK, "zero-sample import accepted; no measurements inserted", "actual delivery evidence comes from the journal or daemon", nil)
			}
		}()
	}
	wg.Wait()
	return r.Snapshot(time.Now())
}
func inspectMQTT(ctx context.Context, c config.Config, probe bool, r *diagnostic.Registry) {
	var topics []string
	for _, d := range c.Devices {
		if d.Active() {
			topics = append(topics, d.Topics.Telemetry)
			if c.Runtime.ObserveCommands && d.Topics.Commands != "" {
				topics = append(topics, d.Topics.Commands)
			}
		}
	}
	probeTopic := ""
	if probe {
		prefix := c.Outputs.HomeAssistant.StatePrefix
		if prefix == "" || strings.ContainsAny(prefix, "+#\x00") || strings.HasSuffix(prefix, "/") {
			r.Set("config", diagnostic.Failed, "probe requires an exact nonempty state_prefix without trailing slash", "set outputs.home_assistant.state_prefix", nil)
			return
		}
		// Never send test messages to a device topic.
		probeTopic = c.Outputs.HomeAssistant.StatePrefix + "/diagnostics/" + uuid.NewString()
		for _, d := range c.Devices {
			if probeTopic == d.Topics.Commands || probeTopic == d.Topics.Telemetry {
				r.Set("mqtt.subscriptions", diagnostic.Failed, "diagnostic topic collides with a device topic", "change state_prefix", nil)
				return
			}
		}
		r.Register("mqtt.probe_publish", true, 0, 0)
	}
	check, stop := context.WithTimeout(ctx, c.Health.CheckTimeout.Duration())
	defer stop()
	messages, err := transport.Inspect(check, c.MQTT, topics, probeTopic)
	if err != nil {
		id := "mqtt.subscriptions"
		if strings.HasPrefix(err.Error(), "MQTT connect") || strings.Contains(err.Error(), "TLS configuration") {
			id = "mqtt.connection"
		} else {
			r.Set("mqtt.connection", diagnostic.OK, "MQTT diagnostic connection accepted", "", nil)
		}
		r.Set(id, diagnostic.Failed, err.Error(), "check MQTT credentials, connectivity and the listed topic/action ACLs", nil)
		if probe {
			r.Set("mqtt.probe_publish", diagnostic.Failed, "diagnostic round trip not verified", "check publish/subscribe permission on the diagnostic topic", nil)
		}
		return
	}
	r.Set("mqtt.connection", diagnostic.OK, "MQTT diagnostic connection accepted", "", nil)
	r.Set("mqtt.subscriptions", diagnostic.OK, "configured capture subscriptions accepted", "", map[string]int64{"topics": int64(len(topics)), "observed_topics": int64(len(messages))})
	if len(messages) == 0 {
		r.Set("mqtt.traffic", diagnostic.Warning, "no device message observed during bounded check", "devices may report only every few minutes; this is not proof of an ACL denial", nil)
	}
	r.Set("mqtt.device_publish", diagnostic.Unknown, "device command/reply publish permissions were not tested", "review downlink ACLs; doctor never sends dummy device commands", nil)
	if probe {
		r.Set("mqtt.probe_publish", diagnostic.OK, "nonretained diagnostic nonce received", "this verifies only the diagnostic topic, not device/discovery permissions", nil)
	}
}
func inspectHA(ctx context.Context, c config.Config, expected []output.Publication, r *diagnostic.Registry) {
	h := c.Outputs.HomeAssistant
	// Check birth separately so a denied discovery inspection cannot obscure it.
	birthCtx, stopBirth := context.WithTimeout(ctx, c.Health.CheckTimeout.Duration())
	birth, err := transport.Inspect(birthCtx, c.MQTT, []string{h.BirthTopic}, "")
	stopBirth()
	if err != nil {
		r.Set("ha.subscription", diagnostic.Failed, err.Error(), "allow subscribe for "+h.BirthTopic, nil)
	} else {
		r.Set("ha.subscription", diagnostic.OK, "HA birth subscription accepted", "", nil)
		if m, ok := birth[h.BirthTopic]; ok && string(m.Payload) == "online" {
			r.Set("ha.birth", diagnostic.OK, "HA online status observed; retained status may be old", "", nil)
		} else {
			r.Set("ha.birth", diagnostic.Warning, "no HA online birth observed", "check HA broker connection and birth topic; a nonretained birth may have been missed", nil)
		}
	}
	check, stop := context.WithTimeout(ctx, c.Health.CheckTimeout.Duration())
	output.InspectDiscovery(check, c, expected, r)
	stop()
	topics := []string{output.BridgeTopic(h)}
	for _, p := range expected {
		var payload struct {
			StateTopic string `json:"state_topic"`
		}
		_ = json.Unmarshal(p.Payload, &payload)
		topics = append(topics, payload.StateTopic, strings.TrimSuffix(payload.StateTopic, "/state")+"/availability")
	}
	check, stop = context.WithTimeout(ctx, c.Health.CheckTimeout.Duration())
	messages, err := transport.Inspect(check, c.MQTT, topics, "")
	stop()
	if err != nil {
		r.Set("ha.availability", diagnostic.Warning, err.Error(), "allow subscribe for state and availability topics", nil)
		return
	}
	offline, missing := int64(0), int64(0)
	for _, topic := range topics {
		if !strings.HasSuffix(topic, "/availability") {
			continue
		}
		m, ok := messages[topic]
		if !ok {
			missing++
		} else if string(m.Payload) != "online" {
			offline++
		}
	}
	status := diagnostic.OK
	reason := "bridge and sensor availability are online"
	if offline+missing > 0 {
		status = diagnostic.Warning
		reason = "some availability topics are offline or absent"
	}
	r.Set("ha.availability", status, reason, "check capture readiness, device timestamps, quality and expire_after", map[string]int64{"offline": offline, "missing": missing})
	var stale int64
	for _, p := range expected {
		var cfg struct {
			StateTopic  string `json:"state_topic"`
			ExpireAfter int64  `json:"expire_after"`
		}
		_ = json.Unmarshal(p.Payload, &cfg)
		if m, ok := messages[cfg.StateTopic]; ok {
			var state struct {
				Timestamp int64  `json:"timestamp"`
				Quality   string `json:"quality"`
			}
			if json.Unmarshal(m.Payload, &state) != nil || time.Now().Unix()-state.Timestamp > cfg.ExpireAfter || state.Timestamp > time.Now().Add(5*time.Minute).Unix() || !output.GoodQuality(state.Quality) {
				stale++
			}
		}
	}
	if stale > 0 {
		r.Set("ha.freshness", diagnostic.Warning, fmt.Sprintf("%d observed states are stale, invalid or incompatible", stale), "check device clocks and sensor quality; nonretained states may not arrive during inspection", nil)
	}
	r.Set("ha.application", diagnostic.Unknown, "broker discovery does not prove HA entity registration", "check HA's separate MQTT ACLs and logs for rejected payloads or duplicate unique IDs", nil)
}

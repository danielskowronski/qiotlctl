package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/settings"
)

func (c Config) Validate(online bool) error {
	if err := c.Health.validate(); err != nil {
		return err
	}
	if err := c.validateOutputs(online); err != nil {
		return err
	}
	if c.APIVersion != "qiotl/v1alpha1" {
		return errors.New("unsupported api_version; expected qiotl/v1alpha1")
	}
	if c.Runtime.Mode != "observe" && c.Runtime.Mode != "respond" {
		return errors.New("runtime.mode must be observe or respond")
	}
	if c.Commands.Timeout.Duration() <= 0 {
		return errors.New("commands.timeout must be positive")
	}
	if c.Runtime.QueueSize < 1 || c.Runtime.QueueSize > 100000 {
		return errors.New("runtime.queue_size must be 1..100000")
	}
	if c.MQTT.QoS > 1 {
		return errors.New("mqtt.qos must be 0 or 1")
	}
	if c.MQTT.KeepAlive.Duration() < time.Second || c.MQTT.KeepAlive.Duration() > 65535*time.Second || c.MQTT.KeepAlive.Duration()%time.Second != 0 {
		return errors.New("mqtt.keepalive must be integral seconds in 1s..65535s")
	}
	if c.MQTT.ConnectTimeout.Duration() <= 0 || c.MQTT.ReconnectMax.Duration() <= 0 {
		return errors.New("MQTT timeouts must be positive")
	}
	if (c.MQTT.TLS.CertFile == "") != (c.MQTT.TLS.KeyFile == "") {
		return errors.New("TLS client certificate and key must be supplied together")
	}
	if c.MQTT.URL != "" {
		u, err := url.Parse(c.MQTT.URL)
		if err != nil || u.Hostname() == "" {
			return errors.New("invalid mqtt.url")
		}
		if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("mqtt.url must not contain credentials, query or fragment")
		}
		switch u.Scheme {
		case "mqtt", "mqtts", "tcp", "ssl", "tls", "ws", "wss":
		default:
			return errors.New("unsupported MQTT URL scheme")
		}
	}
	aliases := map[string]bool{}
	ids := map[string]bool{}
	topics := map[string]string{}
	active := 0
	for alias, d := range c.Devices {
		if alias == "" {
			return errors.New("device aliases must be nonempty")
		}
		fold := strings.ToLower(alias)
		if aliases[fold] {
			return errors.New("device aliases must be unique ignoring case")
		}
		aliases[fold] = true
		if d.ID == "" || ids[d.ID] {
			return fmt.Errorf("missing or duplicate ID for device %s", alias)
		}
		ids[d.ID] = true
		if d.Protocol != "hex" && d.Protocol != "json" && d.Protocol != "auto" {
			return fmt.Errorf("device %s: protocol must be hex, json or auto", alias)
		}
		p := device.Lookup(d.Model)
		if p.Protocol != "auto" && d.Protocol != "auto" && p.Protocol != d.Protocol {
			return fmt.Errorf("device %s: model/protocol conflict", alias)
		}
		if len(d.Settings) > 0 {
			kind := d.Protocol
			if kind == "auto" {
				kind = p.Protocol
			}
			if _, err := settings.Build(kind, d.Settings, c.settingsOptions); err != nil {
				return fmt.Errorf("device %s settings: %w", alias, err)
			}
		}
		if !d.Active() {
			continue
		}
		active++
		if d.Responses.Clock != "" && d.Responses.Clock != "off" && d.Responses.Clock != "type18-heartbeat" {
			return fmt.Errorf("device %s: responses.clock must be off or type18-heartbeat", alias)
		}
		if d.Responses.MinInterval != 0 && d.Responses.MinInterval.Duration() < time.Minute {
			return fmt.Errorf("device %s: responses.min_interval must be at least 1m", alias)
		}
		if d.Responses.Clock == "type18-heartbeat" && p.Model != "CGDN1" {
			return fmt.Errorf("device %s: clock workaround requires CGDN1 profile", alias)
		}
		if online && d.Respond(c.Runtime.Mode) && p.Model == "CGDN1" && d.Topics.Commands == "" {
			return fmt.Errorf("device %s: automatic replies require a command topic", alias)
		}
		if d.Topics.Telemetry == "" {
			return fmt.Errorf("device %s requires a telemetry topic", alias)
		}
		for _, topic := range []string{d.Topics.Telemetry, d.Topics.Commands} {
			if topic == "" {
				continue
			}
			if strings.ContainsAny(topic, "+#\x00") {
				return fmt.Errorf("device %s requires exact topics without wildcards", alias)
			}
			if prev, ok := topics[topic]; ok {
				return fmt.Errorf("topic collision between devices %s and %s", prev, alias)
			}
			topics[topic] = alias
		}
	}
	if online && (c.MQTT.URL == "" || active == 0) {
		return errors.New("online operation requires an MQTT URL and at least one enabled device")
	}
	return nil
}

func (c Config) Redacted() Config {
	if u, err := url.Parse(c.Outputs.VictoriaMetrics.ImportEndpoint); err == nil && u.RawQuery != "" {
		q := u.Query()
		for key := range q {
			q[key] = []string{"[redacted]"}
		}
		u.RawQuery = q.Encode()
		c.Outputs.VictoriaMetrics.ImportEndpoint = u.String()
	}
	c.Outputs.VictoriaMetrics.Password = "[redacted]"
	c.Outputs.VictoriaMetrics.Username = "[redacted]"
	c.MQTT.Password = "[redacted]"
	c.MQTT.Username = "[redacted]"
	devices := map[string]Device{}
	for alias, d := range c.Devices {
		d.Settings = settings.Clone(d.Settings)
		if _, ok := d.Settings["raw"]; ok {
			d.Settings["raw"] = "[raw values omitted]"
		}
		devices[alias] = d
	}
	c.Devices = devices
	return c
}
func redactError(s string, root map[string]any) string {
	var walk func(map[string]any)
	walk = func(m map[string]any) {
		for k, v := range m {
			if c, ok := v.(map[string]any); ok {
				walk(c)
			} else if text, ok := v.(string); ok && text != "" && (strings.Contains(k, "pass") || k == "username" || k == "url") {
				s = strings.ReplaceAll(s, text, "[redacted]")
			}
		}
	}
	walk(root)
	return s
}

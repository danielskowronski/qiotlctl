package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/settings"
)

func writeConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSettingsLimitPolicyIsPerLoad(t *testing.T) {
	dir := t.TempDir()
	base := writeConfig(t, dir, "base.yaml", `mqtt: {url: 'mqtt://example.test'}
devices:
  L:
    model: CGDN1
    topics: {telemetry: l/up, commands: l/down}
    settings: {display: {off_after: 120s}}
`)
	overlay := writeConfig(t, dir, "overlay.yaml", `devices: {L: {settings: {display: {off_after: 301s}}}}`)
	paths := []string{base, overlay}
	if _, err := Load(paths); err == nil {
		t.Fatal("accepted default-invalid merged settings")
	}
	c, err := Load(paths, settings.Options{IgnoreOfficialLimits: true})
	if err != nil {
		t.Fatal(err)
	}
	copy := c
	if err := copy.Validate(true); err != nil {
		t.Fatal("validation policy lost on config copy/revalidation", err)
	}
	b, err := json.Marshal(c.Redacted())
	if err != nil || strings.Contains(string(b), "IgnoreOfficialLimits") || strings.Contains(string(b), "settingsOptions") {
		t.Fatal("policy leaked into serialized configuration", err)
	}
	if _, err := Load(paths); err == nil {
		t.Fatal("opt-in leaked to another load")
	}
	invalid := writeConfig(t, dir, "invalid.yaml", `devices: {L: {settings: {display: {off_after: 2147483648s}}}}`)
	if _, err := Load([]string{base, invalid}, settings.Options{IgnoreOfficialLimits: true}); err == nil {
		t.Fatal("wire overflow accepted in config")
	}
}
func TestLayersAndCleanenv(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "parts/10.yaml", `mqtt:
  url: mqtt://example.test
  password_file: secret
  keepalive: 2m
runtime:
  observe_commands: true
devices:
  bedroom:
    model: CGP22C
    mac: 01:02:03:04:05:06
    topics:
      telemetry: bedroom/up
      commands: bedroom/down
`)
	writeConfig(t, dir, "parts/secret", "file-password\n")
	writeConfig(t, dir, "parts/20.yaml", `devices:
  bedroom:
    topics:
      commands: alternate/down
`)
	main := writeConfig(t, dir, "config.yaml", `include: parts/*.yaml
mqtt:
  qos: 0
---
runtime:
  observe_commands: false
`)
	overlay := writeConfig(t, dir, "overlay.yaml", `mqtt:
  keepalive: 3m
`)
	t.Setenv("QIOTLCTL_MQTT_PASSWORD", "env-password")
	t.Setenv("QIOTLCTL_QUEUE_SIZE", "17")
	t.Setenv("QIOTLCTL_MQTT_KEEPALIVE", "45s")
	t.Setenv("QIOTLCTL_STORAGE_CAPTURE_RAW", "false")
	c, err := Load([]string{main, overlay})
	if err != nil {
		t.Fatal(err)
	}
	if c.MQTT.KeepAlive.Duration() != 45*time.Second || c.MQTT.QoS != 0 || c.Runtime.ObserveCommands || c.Runtime.QueueSize != 17 || c.Storage.CaptureRaw {
		t.Fatalf("bad precedence: %+v", c.Redacted())
	}
	d := c.Devices["bedroom"]
	if d.ID != "010203040506" || d.Protocol != "hex" || d.Topics.Telemetry != "bedroom/up" || d.Topics.Commands != "alternate/down" {
		t.Fatal(d)
	}
	if c.MQTT.Password != "env-password" || c.MQTT.PasswordFile != filepath.Join(dir, "parts", "secret") {
		t.Fatal("credential resolution or override failed")
	}
	if len(c.Sources) != 4 {
		t.Fatalf("sources: %v", c.Sources)
	}
	help, err := EnvironmentHelp()
	if err != nil || !strings.Contains(help, "QIOTLCTL_MQTT_KEEPALIVE") {
		t.Fatalf("help: %s %v", help, err)
	}
}
func TestLegacyDevices(t *testing.T) {
	p := writeConfig(t, t.TempDir(), "legacy.yml", `devices:
  - alias: L
    mac: 01:02:03:04:05:06
    topic_up: lite/up
    topic_down: lite/down
    protocol: json
    model: CGDN1
`)
	c, err := Load([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	if c.Devices["L"].Topics.Commands != "lite/down" {
		t.Fatal(c.Devices)
	}
}
func TestInvalidConfiguration(t *testing.T) {
	for name, src := range map[string]string{
		"unknown":   "mqtt:\n  passwrod: secret\n",
		"duplicate": "mqtt:\n  qos: 0\n  qos: 1\n",
		"include":   "include: absent.yaml\n",
		"cycle":     "include: config.yaml\n",
		"type":      "runtime:\n  queue_size: many\n",
		"write":     "runtime:\n  mode: controller\n",
		"collision": "devices:\n  A: {protocol: json, topics: {telemetry: same}}\n  B: {protocol: hex, topics: {telemetry: same}}\n",
		"model":     "devices:\n  A: {model: CGP22C, protocol: json, topics: {telemetry: a}}\n",
		"wildcard":  "devices:\n  A: {topics: {telemetry: 'a/+'}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := writeConfig(t, t.TempDir(), "config.yaml", src)
			if _, err := Load([]string{p}); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
	t.Setenv("QIOTLCTL_MQTT_QOS", "invalid-secret-text")
	_, err := Load(nil)
	if err == nil || strings.Contains(err.Error(), "invalid-secret-text") {
		t.Fatal("bad env accepted or leaked")
	}
}
func TestConnectionFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "broker.json", `{"user":"u","pass":"p","mqtt_uri":"mqtt://example.test"}`)
	p := writeConfig(t, dir, "config.yaml", "mqtt:\n  connection_file: broker.json\n")
	c, err := Load([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	if c.MQTT.Username != "u" || c.MQTT.Password != "p" || c.MQTT.URL != "mqtt://example.test" {
		t.Fatal("connection file was not loaded")
	}
	redacted := c.Redacted()
	if redacted.MQTT.Username == "u" || redacted.MQTT.Password == "p" {
		t.Fatal("credential not redacted")
	}
	t.Setenv("QIOTLCTL_MQTT_URL", "mqtt://override.test")
	c, err = Load([]string{p})
	if err != nil || c.MQTT.URL != "mqtt://override.test" {
		t.Fatal("URL env did not win")
	}
}
func TestPaths(t *testing.T) {
	t.Setenv("QIOTLCTL_CONFIG", "first.yaml"+string(os.PathListSeparator)+"second.yaml")
	paths, err := Paths(nil, false)
	if err != nil || len(paths) != 2 {
		t.Fatal(paths, err)
	}
	paths, err = Paths([]string{"explicit.yaml"}, false)
	if err != nil || len(paths) != 1 || paths[0] != "explicit.yaml" {
		t.Fatal(paths, err)
	}
	if DefaultPath(true) != "/etc/qiotld/config.yaml" {
		t.Fatal(DefaultPath(true))
	}
}

func TestNullAcrossIncludes(t *testing.T) {
	dir := t.TempDir()
	base := writeConfig(t, dir, "base.yaml", "devices:\n  A: {topics: {telemetry: a/up}}\n  B: {topics: {telemetry: b/up}}\n")
	writeConfig(t, dir, "delete.yaml", "devices:\n  A: null\n")
	overlay := writeConfig(t, dir, "overlay.yaml", "include: delete.yaml\n")
	c, err := Load([]string{base, overlay})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Devices) != 1 || c.Devices["B"].Topics.Telemetry != "b/up" {
		t.Fatal(c.Devices)
	}
}

func TestNullRestoresDefault(t *testing.T) {
	p := writeConfig(t, t.TempDir(), "config.yaml", "mqtt:\n  keepalive: null\nruntime: null\n")
	c, err := Load([]string{p})
	if err != nil || c.MQTT.KeepAlive.Duration() != time.Minute || c.Runtime.QueueSize != 1024 {
		t.Fatal(c.Redacted(), err)
	}
}

func TestResponsePolicyLayering(t *testing.T) {
	dir := t.TempDir()
	base := writeConfig(t, dir, "base.yaml", `devices:
  lite:
    model: CGDN1
    topics: {telemetry: fixture/up, commands: fixture/down}
`)
	c, err := Load([]string{base})
	if err != nil {
		t.Fatal(err)
	}
	if c.Devices["lite"].Respond(c.Runtime.Mode) {
		t.Fatal("replies enabled by default")
	}
	layer := writeConfig(t, dir, "override.yaml", `runtime:
  mode: respond
devices:
  lite:
    responses:
      enabled: false
      clock: type18-heartbeat
      min_interval: 5m
commands:
  timeout: 2m
`)
	c, err = Load([]string{base, layer})
	if err != nil {
		t.Fatal(err)
	}
	if c.Devices["lite"].Respond(c.Runtime.Mode) || c.Devices["lite"].ClockInterval() != 5*time.Minute || c.Commands.Timeout.Duration() != 2*time.Minute {
		t.Fatal(c)
	}
	t.Setenv("QIOTLCTL_MODE", "observe")
	t.Setenv("QIOTLCTL_COMMAND_TIMEOUT", "10s")
	c, err = Load([]string{base, layer})
	if err != nil || c.Runtime.Mode != "observe" || c.Commands.Timeout.Duration() != 10*time.Second {
		t.Fatal(c, err)
	}
}

func TestSettingsOverlayAndValidation(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	overlay := filepath.Join(dir, "overlay.yaml")
	if err := os.WriteFile(base, []byte(`devices:
  D:
    model: CGP22C
    topics: {telemetry: d/up, commands: d/down}
    settings:
      reporting: {upload_interval: 5m, record_interval: 30s}
      clock: {sntp_enabled: true}
      calibration: {temperature: {offset_c: 1}}
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte(`devices:
  D:
    settings:
      reporting: {record_interval: null}
      clock: {sntp_enabled: false}
      calibration: {temperature: {offset_c: 0}}
`), 0600); err != nil {
		t.Fatal(err)
	}
	c, e := Load([]string{base, overlay})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(c.Devices["D"].Settings)
	if strings.Contains(string(b), "record_interval") || !strings.Contains(string(b), `"sntp_enabled":false`) || !strings.Contains(string(b), `"offset_c":0`) {
		t.Fatal(string(b))
	}
	if err := os.WriteFile(overlay, []byte("devices:\n  D:\n    settings:\n      reporting: {upload_interval: 90s}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, e = Load([]string{base, overlay}); e == nil {
		t.Fatal("invalid granularity accepted")
	}
}

func TestBareHomeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := expandPath("~")
	if err != nil || got != home {
		t.Fatal(got, err)
	}
}

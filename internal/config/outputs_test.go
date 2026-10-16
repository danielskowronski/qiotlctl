package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestOutputLayersCredentialsAndHAIdentity(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	over := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(filepath.Join(dir, "password"), []byte("file-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(base, []byte(`---
storage: {path: queue.sqlite}
outputs:
  victoriametrics:
    import_endpoint: http://localhost:18428/api/v1/import
    password_file: password
  home_assistant: {expire_after: 600s}
devices:
  bedroom:
    model: CGP22C
    mac: "001122334455"
    topics: {telemetry: b/up}
    home_assistant: {id: bedroom, sensor_expire_after: {signal_strength: 20m}}
`), 0600)
	os.WriteFile(over, []byte("---\noutputs:\n  home_assistant: {state_prefix: my_qingping}\ndevices:\n  bedroom:\n    home_assistant: {name: Qingping Master Bedroom}\n"), 0600)
	t.Setenv("QIOTLCTL_VM_PASSWORD", "env-secret")
	t.Setenv("QIOTLCTL_VM_BATCH_SIZE", "42")
	c, err := Load([]string{base, over})
	if err != nil {
		t.Fatal(err)
	}
	if c.Outputs.VictoriaMetrics.Password != "env-secret" || c.Outputs.VictoriaMetrics.BatchSize != 42 || c.Outputs.VictoriaMetrics.PasswordFile != filepath.Join(dir, "password") {
		t.Fatal("credential/path/env precedence failed")
	}
	d := c.Devices["bedroom"]
	if d.HAID() != "bedroom" || d.HAExpiry(c.Outputs.HomeAssistant, "co2") != 10*time.Minute || d.HAExpiry(c.Outputs.HomeAssistant, "signal_strength") != 20*time.Minute || d.HomeAssistant.Name != "Qingping Master Bedroom" {
		t.Fatal(d)
	}
	b, _ := yaml.Marshal(c.Redacted())
	if strings.Contains(string(b), "env-secret") || strings.Contains(string(b), "file-secret") {
		t.Fatal("credential leaked")
	}
	c.Outputs.HomeAssistant.Enabled = true
	c.Devices["duplicate"] = d
	if err = c.Validate(false); err == nil {
		t.Fatal("duplicate HA ID accepted")
	}
}

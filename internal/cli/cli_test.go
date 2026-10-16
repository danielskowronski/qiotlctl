package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func invoke(t *testing.T, args ...string) (string, error) {
	t.Helper()
	c := New(false, "test")
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs(args)
	c.SetIn(strings.NewReader(""))
	err := c.ExecuteContext(context.Background())
	return out.String(), err
}
func TestOfflineCommands(t *testing.T) {
	out, err := invoke(t, "encode", "settings", "--model", "CGP22C", "--upload-interval", "5m")
	if err != nil || strings.TrimSpace(out) != "43473205000402000500cc00" {
		t.Fatal(out, err)
	}
	out, err = invoke(t, "decode", "--protocol", "hex", "--input-format", "hex", "--direction", "to-device", strings.TrimSpace(out))
	if err != nil || !strings.Contains(out, `"upload_interval_seconds":300`) {
		t.Fatal(out, err)
	}
	out, err = invoke(t, "encode", "settings", "--model", "CGDN1", "--upload-interval", "15m", "--output-format", "json")
	if err != nil || !strings.Contains(out, `"report_interval":900`) {
		t.Fatal(out, err)
	}
	for _, args := range [][]string{{"encode", "settings", "--model", "CGP22C", "--co2-min", "-1", "--co2-max", "800"}, {"encode", "settings", "--model", "CGP22C", "--upload-interval", "0s"}, {"decode", "--input-format", "hex", "0000"}, {"replay"}} {
		if _, err = invoke(t, args...); err == nil {
			t.Fatal("invalid command accepted", args)
		}
	}
}

func TestProfileCLI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`mqtt: {url: 'mqtt://example.test', password: never-print-password}
profiles:
  common: {settings: {display: {off_after: 120s, clock_format: 24h}}}
  lite:
    extends: common
    settings: {reporting: {record_interval: 10s}, raw: {json: {secret: never-print-raw}}}
devices:
  L:
    model: CGDN1
    profile: lite
    topics: {telemetry: l/up, commands: l/down}
    settings: {display: {off_after: 180s, clock_format: null}}
`), 0600); err != nil {
		t.Fatal(err)
	}
	settingsFile := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(settingsFile, []byte("display: {off_after: 240s}\nreporting: {record_interval: null}\nraw: null\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := invoke(t, "-c", path, "config", "explain", "l")
	if err != nil || !strings.Contains(out, `"profile_chain":["common","lite"]`) || !strings.Contains(out, `"removed":true`) || strings.Contains(out, "never-print") {
		t.Fatal(out, err)
	}
	out, err = invoke(t, "-c", path, "config", "show")
	if err != nil || strings.Contains(out, "profiles:") || strings.Contains(out, "profile:") || strings.Contains(out, "never-print") {
		t.Fatal(out, err)
	}
	var shown struct {
		Devices map[string]struct {
			Settings map[string]any `yaml:"settings"`
		} `yaml:"devices"`
	}
	if err := yaml.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatal(err)
	}
	if _, exists := shown.Devices["L"].Settings["display"].(map[string]any)["clock_format"]; exists {
		t.Fatal("removed field reappeared in effective config")
	}
	// Profile < device < settings file < explicit --set. The file can also
	// remove inherited properties, without sending a null/reset to the device.
	args := []string{"-c", path, "encode", "settings", "L", "--from-config", "--settings-file", settingsFile, "--set", "display.off_after=300s", "--output-format", "json"}
	out, err = invoke(t, args...)
	var msg struct {
		Setting map[string]any `json:"setting"`
	}
	if err != nil || json.Unmarshal([]byte(out), &msg) != nil || len(msg.Setting) != 1 || msg.Setting["display_off_time"] != float64(300) {
		t.Fatal(out, err)
	}
	out, err = invoke(t, "-c", path, "apply-settings", "L", "--from-config", "--dry-run")
	if err != nil || !strings.Contains(out, `"value":"3m0s"`) || !strings.Contains(out, `"value":"10s"`) || strings.Contains(out, "never-print") {
		t.Fatal(out, err)
	}
	// Omitting --from-config remains explicit-only, even with a selected profile.
	out, err = invoke(t, "-c", path, "encode", "settings", "L", "--set", "display.off_after=60s", "--output-format", "json")
	msg.Setting = nil
	if err != nil || json.Unmarshal([]byte(out), &msg) != nil || len(msg.Setting) != 1 || msg.Setting["display_off_time"] != float64(60) {
		t.Fatal(out, err)
	}
}

func TestIgnoreOfficialLimitsCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`mqtt: {url: 'tcp://127.0.0.1:1'}
devices:
  L:
    model: CGDN1
    topics: {telemetry: l/up, commands: l/down}
    settings: {display: {off_after: 301s}}
  D:
    model: CGP22C
    topics: {telemetry: d/up, commands: d/down}
`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-c", path, "config", "validate"}, "configuration valid"},
		{[]string{"-c", path, "apply-settings", "L", "--from-config", "--dry-run"}, `"ignore_official_limits":true`},
		{[]string{"-c", path, "encode", "settings", "L", "--from-config", "--output-format", "json"}, `"display_off_time":301`},
		{[]string{"encode", "settings", "--protocol", "hex", "--upload-interval", "0s"}, "43473205000402000000c700"},
		{[]string{"-c", path, "settings", "get", "D", "--reassert-upload-interval", "0s", "--dry-run"}, `"completion":"settings-report"`},
		{[]string{"-c", path, "apply-settings", "D", "--set", "display.clock_format=2", "--readback-upload-interval", "0s", "--dry-run"}, `"hex_readback_upload_requested":true`},
		{[]string{"-c", path, "command", "L", "realtime", "--duration", "30s", "--interval", "1s", "--dry-run"}, `"ignore_official_limits":true`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			if _, err := invoke(t, tc.args...); err == nil {
				t.Fatal("accepted without opt-in")
			}
			args := append(append([]string(nil), tc.args...), "--ignore-official-limits")
			out, err := invoke(t, args...)
			if err != nil || !strings.Contains(out, tc.want) {
				t.Fatal(out, err)
			}
			if _, err := invoke(t, tc.args...); err == nil {
				t.Fatal("validation policy leaked to another invocation")
			}
		})
	}
	for _, args := range [][]string{
		{"encode", "settings", "--protocol", "hex", "--upload-interval", "90s"},
		{"encode", "settings", "--protocol", "hex", "--set", "display.clock_format=256"},
		{"-c", path, "command", "L", "realtime", "--interval", "1.5s", "--dry-run"},
		{"-c", path, "settings", "get", "D", "--reassert-upload-interval", "65536m", "--dry-run"},
	} {
		if _, err := invoke(t, append(args, "--ignore-official-limits")...); err == nil {
			t.Fatal("invalid encoding accepted", args)
		}
	}
}

func TestCommandDryRunAndExitCodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`devices:
  L:
    model: CGDN1
    topics: {telemetry: fixture/up, commands: fixture/down}
  B:
    model: CGP22C
    topics: {telemetry: binary/up, commands: binary/down}
`), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out, err := invoke(t, "-c", path, "settings", "get", "L", "--dry-run")
	if err != nil || !strings.Contains(out, `"completion":"settings-report"`) || time.Since(start) > time.Second {
		t.Fatal(out, err)
	}
	_, err = invoke(t, "-c", path, "command", "B", "realtime", "--dry-run")
	if ExitCode(err) != 3 {
		t.Fatal(err)
	}
	_, err = invoke(t, "-c", path, "command", "L", "sync-time", "--duration", "1m", "--dry-run")
	if ExitCode(err) != 2 {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "raw.json")
	if err = os.WriteFile(file, []byte(`{"type":"17","id":1,"setting":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err = invoke(t, "encode", "raw", "--protocol", "json", "--file", file, "--new-request-id", "--output-format", "json")
		var value map[string]any
		if err != nil || json.Unmarshal([]byte(out), &value) != nil || value["id"] == nil {
			t.Fatal(out, err)
		}
	}
}

func TestSettingsCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`devices:
  D:
    model: CGP22C
    topics: {telemetry: d/up, commands: d/down}
    settings:
      display: {clock_format: 24h}
      raw: {hex: [{key: '0x06', value_hex: 'e803'}]}
`), 0600); err != nil {
		t.Fatal(err)
	}
	out, e := invoke(t, "-c", path, "apply-settings", "D", "--from-config", "--dry-run", "--set", "display.clock_format=12h")
	if e != nil || !strings.Contains(out, `"value":"12h"`) || strings.Contains(out, "e803") {
		t.Fatal(out, e)
	}
	out, e = invoke(t, "-c", path, "encode", "settings", "D", "--from-config")
	if e != nil || !strings.Contains(out, "3e010000") || !strings.Contains(out, "060200e803") {
		t.Fatal(out, e)
	}
	out, e = invoke(t, "encode", "settings", "--protocol", "json", "--raw-json", `{"a":false,"b":0,"c":"00","d":null}`, "--output-format", "json")
	if e != nil || !strings.Contains(out, `"d":null`) || !strings.Contains(out, `"c":"00"`) {
		t.Fatal(out, e)
	}
	for _, args := range [][]string{
		{"-c", path, "apply-settings", "D", "--set", "display.unknown=true", "--dry-run"},
		{"-c", path, "apply-settings", "D", "--from-config", "--check", "--dry-run"},
		{"-c", path, "apply-settings", "D", "--set", "reporting.upload_interval=10m", "--readback-upload-interval", "5m", "--dry-run"},
		{"-c", path, "apply-settings", "D", "--from-config", "--raw-hex", "0x3e=00", "--dry-run"},
	} {
		if _, e = invoke(t, args...); e == nil {
			t.Fatal("accepted invalid settings", args)
		}
	}
	out, e = invoke(t, "-c", path, "settings", "get", "D", "--reassert-upload-interval", "5m", "--dry-run")
	if e != nil || !strings.Contains(out, `"operation":"settings-probe"`) {
		t.Fatal(out, e)
	}
}

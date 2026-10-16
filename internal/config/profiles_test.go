package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/settings"
	"gopkg.in/yaml.v3"
)

func TestProfileLayersAndProvenance(t *testing.T) {
	dir := t.TempDir()
	base := writeConfig(t, dir, "profiles/base.yaml", `profiles:
  common:
    settings:
      reporting: {upload_interval: 5m, record_interval: 30s}
      display: {clock_format: 24h, temperature_unit: celsius}
      clock: {sntp_enabled: true}
      calibration: {temperature: {offset_c: 1}}
      raw: {hex: [{key: '0x06', value_hex: 'e803'}, {key: '0x6a', value_hex: '00'}]}
  standard_co2:
    extends: common
    settings: {reporting: {record_interval: 60s}}
---
profiles:
  common:
    settings: {display: {temperature_unit: fahrenheit}}
`)
	devices := writeConfig(t, dir, "devices.yaml", `devices:
  D:
    model: CGP22C
    profile: standard_co2
    topics: {telemetry: d/up, commands: d/down}
    settings: {reporting: {upload_interval: 10m}}
  B:
    model: CGP22C
    profile: standard_co2
    topics: {telemetry: b/up, commands: b/down}
`)
	main := writeConfig(t, dir, "config.yaml", `include: [devices.yaml, 'profiles/*.yaml']
mqtt: {url: 'mqtt://example.test', keepalive: 30s}
`)
	removed := writeConfig(t, dir, "remove.yaml", `devices:
  D:
    settings:
      reporting: {record_interval: null}
      clock: {sntp_enabled: false}
      calibration: {temperature: {offset_c: 0}}
      raw: {hex: [{key: '0x6a', value_hex: '01'}]}
`)
	overlay := writeConfig(t, dir, "overlay.yaml", `include: remove.yaml
profiles:
  common:
    settings: {reporting: {upload_interval: 15m}}
`)
	t.Setenv("QIOTLCTL_MQTT_KEEPALIVE", "45s")
	c, err := Load([]string{main, overlay})
	if err != nil {
		t.Fatal(err)
	}
	if c.MQTT.KeepAlive.Duration() != 45*time.Second || c.Devices["D"].Respond(c.Runtime.Mode) {
		t.Fatal("environment precedence or response policy changed")
	}
	for alias, want := range map[string]string{
		"D": `{"calibration":{"temperature":{"offset_c":0}},"clock":{"sntp_enabled":false},"display":{"clock_format":"24h","temperature_unit":"fahrenheit"},"raw":{"hex":[{"key":"0x6a","value_hex":"01"}]},"reporting":{"upload_interval":"10m"}}`,
		"B": `{"calibration":{"temperature":{"offset_c":1}},"clock":{"sntp_enabled":true},"display":{"clock_format":"24h","temperature_unit":"fahrenheit"},"raw":{"hex":[{"key":"0x06","value_hex":"e803"},{"key":"0x6a","value_hex":"00"}]},"reporting":{"record_interval":"60s","upload_interval":"15m"}}`,
	} {
		got, _ := json.Marshal(c.Devices[alias].Settings)
		if string(got) != want {
			t.Fatalf("%s: %s", alias, got)
		}
	}
	e, err := c.Explain("d")
	if err != nil || strings.Join(e.ProfileChain, ",") != "common,standard_co2" || e.ProfileSource.File != devices {
		t.Fatal(e, err)
	}
	for path, want := range map[string]Source{
		"settings.reporting.upload_interval": {File: devices, Document: 1, Path: "devices.D.settings.reporting.upload_interval"},
		"settings.reporting.record_interval": {File: removed, Document: 1, Path: "devices.D.settings.reporting.record_interval", Removed: true},
		"settings.display.temperature_unit":  {File: base, Document: 2, Path: "profiles.common.settings.display.temperature_unit"},
		"settings.raw.hex":                   {File: removed, Document: 1, Path: "devices.D.settings.raw.hex"},
	} {
		if e.Sources[path] != want {
			t.Fatalf("%s source: %+v, want %+v", path, e.Sources[path], want)
		}
	}
	encoded, _ := json.Marshal(e)
	if strings.Contains(string(encoded), "value_hex") || strings.Contains(string(encoded), "e803") {
		t.Fatal("raw values leaked through explanation")
	}
	// The runtime/show contract has no profile references that could reapply a
	// deleted setting on another load. Each device owns its expanded maps.
	if err := settings.Set(c.Devices["D"].Settings, "display.clock_format", "12h"); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c.Devices["B"].Settings)
	if !strings.Contains(string(b), `"clock_format":"24h"`) {
		t.Fatal("device override mutated another device")
	}
	show, err := yaml.Marshal(c.Redacted())
	if err != nil || strings.Contains(string(show), "profile:") || strings.Contains(string(show), "profiles:") || strings.Contains(string(show), "e803") {
		t.Fatal("authored profiles or raw values leaked into effective config", err)
	}
	if _, err := c.Explain("missing"); err == nil {
		t.Fatal("unknown alias accepted")
	}
}

func TestProfileRemovalAndReplacement(t *testing.T) {
	for _, tc := range []struct{ name, extra, want string }{
		{"property", "devices: {D: {settings: {display: {clock_format: null}}}}", `{"display":{"temperature_unit":"celsius"}}`},
		{"subtree", "devices: {D: {settings: {display: null}}}", `{}`},
		{"all-settings", "devices: {D: {settings: null}}", `null`},
		{"selection", "devices: {D: {profile: null}}", `null`},
		{"child-removal", "profiles: {child: {settings: {display: {clock_format: null}}}}", `{"display":{"temperature_unit":"celsius"}}`},
		{"child-all-removal", "profiles: {child: {settings: null}}", `null`},
		{"parent-selection", "profiles: {child: {extends: null}}", `null`},
		{"empty-map-inherits", "devices: {D: {settings: {}}}", `{"display":{"clock_format":"24h","temperature_unit":"celsius"}}`},
		{"switch-profile", "devices: {D: {profile: alternative}}", `{"display":{"clock_format":"12h"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base := writeConfig(t, dir, "base.yaml", `profiles:
  common: {settings: {display: {clock_format: 24h, temperature_unit: celsius}}}
  child: {extends: common}
  alternative: {settings: {display: {clock_format: 12h}}}
devices:
  D: {model: CGP22C, profile: child, topics: {telemetry: d/up}}
`)
			overlay := writeConfig(t, dir, "overlay.yaml", tc.extra)
			c, err := Load([]string{base, overlay})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(c.Devices["D"].Settings)
			if string(b) != tc.want {
				t.Fatalf("got %s, want %s", b, tc.want)
			}
		})
	}
}

func TestInvalidProfiles(t *testing.T) {
	for name, text := range map[string]string{
		"unknown":                  "devices: {D: {profile: missing}}",
		"case-sensitive":           "profiles: {Standard: {}}\ndevices: {D: {profile: standard}}",
		"list-selection":           "devices: {D: {profile: [one, two]}}",
		"numeric-selection":        "devices: {D: {profile: 5}}",
		"unknown-parent":           "profiles: {unused: {extends: missing}}",
		"self-cycle":               "profiles: {p: {extends: p}}",
		"cycle":                    "profiles: {a: {extends: b}, b: {extends: c}, c: {extends: a}}",
		"unknown-field":            "profiles: {p: {settigns: {}}}",
		"model-in-profile":         "profiles: {p: {model: CGP22C}}",
		"wrong-settings-type":      "profiles: {p: {settings: [one]}}",
		"wrong-profile-type":       "profiles: {p: []}",
		"wrong-profiles-type":      "profiles: []",
		"empty-name":               "profiles: {'': {}}",
		"parent-list":              "profiles: {p: {extends: [a, b]}}",
		"range":                    "profiles: {p: {settings: {reporting: {upload_interval: 0s}}}}\ndevices: {D: {profile: p}}",
		"unknown-setting":          "profiles: {p: {settings: {display: {typo: 1}}}}\ndevices: {D: {profile: p}}",
		"unsupported-mapping":      "profiles: {p: {settings: {display: {off_after: 1m}}}}\ndevices: {D: {profile: p}}",
		"disabled-still-validates": "profiles: {p: {settings: {display: {off_after: 1m}}}}\ndevices: {D: {profile: p, enabled: false}}",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			base := writeConfig(t, dir, "base.yaml", "devices: {D: {model: CGP22C, topics: {telemetry: d/up}}}")
			layer := writeConfig(t, dir, "layer.yaml", text)
			if _, err := Load([]string{base, layer}); err == nil {
				t.Fatal("invalid profile config accepted")
			}
		})
	}
	// Both recursive traversal and a chain assembled from cached parents are bounded.
	for _, reverse := range []bool{false, true} {
		var b strings.Builder
		b.WriteString("profiles:\n")
		for i := 0; i <= 32; i++ {
			root, parent := i == 0, i-1
			if reverse {
				root, parent = i == 32, i+1
			}
			if root {
				fmt.Fprintf(&b, "  p%02d: {}\n", i)
			} else {
				fmt.Fprintf(&b, "  p%02d: {extends: p%02d}\n", i, parent)
			}
		}
		path := writeConfig(t, t.TempDir(), "deep.yaml", b.String())
		if _, err := Load([]string{path}); err == nil || !strings.Contains(err.Error(), "depth") {
			t.Fatal("inheritance depth not bounded", err)
		}
	}
}

func TestProfilesLegacyLimitOverrideAndDeletion(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir, "base.yaml", `profiles:
  p: {settings: {reporting: {upload_interval: 0s}}}
devices:
  - {alias: D, model: CGP22C, profile: p, topic_up: d/up, topic_down: d/down}
`)
	c, err := Load([]string{p}, settings.Options{IgnoreOfficialLimits: true})
	if err != nil || c.Devices["D"].Topics.Commands != "d/down" {
		t.Fatal(err)
	}
	if err := c.Validate(false); err != nil {
		t.Fatal("limit policy lost", err)
	}
	removed := writeConfig(t, dir, "removed.yaml", "profiles: {p: null}\n")
	if _, err := Load([]string{p, removed}); err == nil || !strings.Contains(err.Error(), "unknown settings profile") {
		t.Fatal("deleted referenced profile accepted", err)
	}
	writeConfig(t, dir, "removed.yaml", "profiles: {p: null}\ndevices: {D: null}\n")
	c, err = Load([]string{p, removed})
	if err != nil || len(c.Devices) != 0 {
		t.Fatal("removed device resurrected", err)
	}
	// Paths are resolved at the file that defined them, independent of expansion.
	main := writeConfig(t, dir, "config.yaml", "include: sub/defs.yaml\n")
	writeConfig(t, dir, "sub/defs.yaml", "profiles: {p: {settings: {display: {clock_format: 24h}}}}\nstorage: {path: captures.sqlite}\n")
	c, err = Load([]string{main})
	if err != nil || c.Storage.Path != filepath.Join(dir, "sub/captures.sqlite") {
		t.Fatal("relative path provenance changed", err)
	}
}

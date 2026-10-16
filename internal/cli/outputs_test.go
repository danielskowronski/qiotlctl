package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHAPreviewOfflineAndConfiguredIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(`---
mqtt: {url: mqtt://127.0.0.1:1}
devices:
  bedroom:
    mac: "001122334455"
    model: CGP22C
    topics: {telemetry: qingping/001122334455/up}
    home_assistant: {id: bedroom}
`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	out, err := invoke(t, "-c", path, "ha", "preview")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"qingping_bedroom_battery", "qingping_001122334455", "Qingping Bedroom", "CGP22C", "CO2", "expire_after\":600"} {
		if !strings.Contains(out, want) {
			t.Fatal(want, out)
		}
	}
	if strings.Contains(out, `"state_topic":"qingping/001122334455/up"`) {
		t.Fatal("discovery still points at raw binary telemetry")
	}
	if _, err = invoke(t, "-c", path, "backfill", "vm", "--dry-run", "--sqlite", "missing", "--since", "2026-01-01T00:00:00Z", "--until", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("equal time bounds accepted")
	}
}

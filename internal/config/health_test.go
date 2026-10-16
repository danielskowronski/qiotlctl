package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthConfigLayersAndEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("health: {require_ha: false, failure_grace: 0s, max_pending_age: 10m}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QIOTLCTL_HEALTH_CHECK_INTERVAL", "10s")
	c, err := Load([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	if c.Health.RequireHA || !c.Health.RequireVM || c.Health.FailureGrace != 0 || c.Health.CheckInterval.Duration() != 10*time.Second {
		t.Fatal(c.Health)
	}
	t.Setenv("QIOTLCTL_HEALTH_CHECK_TIMEOUT", "11s")
	if _, err = Load([]string{p}); err == nil {
		t.Fatal("timeout exceeding interval accepted")
	}
}

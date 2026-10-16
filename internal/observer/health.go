package observer

import (
	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"time"
)

// InitHealth must run before starting the HTTP server or workers.
func (s *Stats) InitHealth(c config.Config) {
	r := diagnostic.New()
	s.Diagnostics = r
	s.Outputs.Diagnostics = r
	for _, id := range []string{"mqtt.connection", "mqtt.subscriptions", "storage"} {
		r.Register(id, true, 0, 0)
	}
	for _, group := range []struct {
		prefix            string
		enabled, required bool
		ids               []string
	}{
		{"ha", c.Outputs.HomeAssistant.Enabled, c.Health.RequireHA, []string{"connection", "subscription", "publication", "discovery"}},
		{"vm", c.Outputs.VictoriaMetrics.Enabled, c.Health.RequireVM, []string{"endpoint", "delivery", "backlog"}},
	} {
		for _, name := range group.ids {
			id := group.prefix + "." + name
			var age time.Duration
			if name == "endpoint" || name == "discovery" {
				age = 3*c.Health.CheckInterval.Duration() + c.Health.CheckTimeout.Duration()
			}
			r.Register(id, group.enabled && group.required, c.Health.FailureGrace.Duration(), age)
			if !group.enabled {
				r.Set(id, diagnostic.Disabled, "output disabled", "", nil)
			}
		}
	}
}
func (s *Stats) CaptureReady() bool {
	return s.Connected.Load() && s.Subscribed.Load() && s.StorageOK.Load()
}
func (s *Stats) Ready() bool {
	if s.Diagnostics == nil {
		return s.CaptureReady()
	}
	return s.CaptureReady() && s.Diagnostics.Snapshot(time.Now()).Ready
}

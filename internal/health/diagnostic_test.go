package health

import (
	"encoding/json"
	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/observer"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExporterReadinessDoesNotChangeLivenessOrCapture(t *testing.T) {
	c, _ := config.Load(nil)
	c.Outputs.VictoriaMetrics.Enabled = true
	c.Health.FailureGrace = 0
	s := &observer.Stats{}
	s.InitHealth(c)
	s.Connected.Store(true)
	s.Subscribed.Store(true)
	s.StorageOK.Store(true)
	for _, id := range []string{"mqtt.connection", "mqtt.subscriptions", "storage", "vm.endpoint", "vm.delivery", "vm.backlog"} {
		s.Diagnostics.Set(id, diagnostic.OK, "ok", "", nil)
	}
	s.Diagnostics.Set("vm.delivery", diagnostic.Failed, "HTTP 401: VM authentication/authorization rejected", "check VMUser credentials", nil)
	if !s.CaptureReady() || s.Ready() {
		t.Fatal("capture and aggregate readiness coupled")
	}
	h := Handler(s)
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
		if w.Code != 503 || !strings.Contains(w.Body.String(), "check VMUser credentials") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/livez", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	var snapshot diagnostic.Snapshot
	if json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || snapshot.Ready {
		t.Fatal(w.Body.String())
	}
	s.Diagnostics.Set("vm.delivery", diagnostic.OK, "delivery recovered", "", nil)
	if !s.Ready() {
		t.Fatal(s.Diagnostics.Snapshot(time.Now()))
	}
}

func TestDisabledAndAdvisoryOutputs(t *testing.T) {
	c, _ := config.Load(nil)
	c.Outputs.HomeAssistant.Enabled = true
	c.Health.RequireHA = false
	s := &observer.Stats{}
	s.InitHealth(c)
	s.Connected.Store(true)
	s.Subscribed.Store(true)
	s.StorageOK.Store(true)
	for _, id := range []string{"mqtt.connection", "mqtt.subscriptions", "storage"} {
		s.Diagnostics.Set(id, diagnostic.OK, "ok", "", nil)
	}
	s.Diagnostics.Set("ha.discovery", diagnostic.Failed, "retained discovery missing", "check ACL", nil)
	if !s.Ready() {
		t.Fatal("advisory HA or disabled VM blocked capture readiness")
	}
	for _, check := range s.Diagnostics.Snapshot(time.Now()).Checks {
		if strings.HasPrefix(check.ID, "vm.") && check.Status != diagnostic.Disabled {
			t.Fatal(check)
		}
	}
	// A stale capture atomic must also be reflected in the JSON snapshot.
	s.Connected.Store(false)
	w := httptest.NewRecorder()
	Handler(s).ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	var snapshot diagnostic.Snapshot
	if json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || snapshot.Ready {
		t.Fatal(w.Body.String())
	}
}

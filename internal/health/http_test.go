package health

import (
	"github.com/danielskowronski/qiotlctl/internal/observer"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbesAndMetrics(t *testing.T) {
	s := &observer.Stats{}
	h := Handler(s)
	for path, want := range map[string]int{"/livez": 200, "/readyz": 503} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatal(path, w.Code)
		}
	}
	s.Connected.Store(true)
	s.Subscribed.Store(true)
	s.StorageOK.Store(true)
	s.Received.Add(3)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "qiotl_messages_received_total 3") {
		t.Fatal(w.Body.String())
	}
	s.Connected.Store(false)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

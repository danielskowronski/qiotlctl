package output

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/transport"
)

func TestDiscoveryEvidence(t *testing.T) {
	s, _, _ := fixture(t)
	expected := Preview(s.Config)
	m := map[string]transport.Observed{}
	for _, p := range expected {
		m[p.Topic] = transport.Observed{Payload: p.Payload, Retained: true}
	}
	if status, _, _ := CheckDiscovery(expected, m); status != diagnostic.OK {
		t.Fatal(status)
	}
	delete(m, expected[0].Topic)
	if status, reason, counts := CheckDiscovery(expected, m); status != diagnostic.Failed || !strings.Contains(reason, expected[0].Topic) || counts["missing"] != 1 {
		t.Fatal(status, reason, counts)
	}
	m[expected[0].Topic] = transport.Observed{Payload: []byte(`{"unique_id":"wrong"}`), Retained: true}
	if _, _, counts := CheckDiscovery(expected, m); counts["mismatched"] != 1 {
		t.Fatal(counts)
	}
	m[expected[0].Topic] = transport.Observed{Payload: expected[0].Payload, Retained: false}
	if _, _, counts := CheckDiscovery(expected, m); counts["missing"] != 1 {
		t.Fatal("nonretained echo treated as retained evidence")
	}
}
func TestVMIdleProbeCannotClearFailedBatch(t *testing.T) {
	s, d, now := fixture(t)
	capture(t, s, event(d, now, 500))
	s.Config.Health.CheckInterval = config.Duration(20 * time.Millisecond)
	s.Config.Health.CheckTimeout = config.Duration(10 * time.Millisecond)
	s.Config.Outputs.VictoriaMetrics.RetryMax = config.Duration(40 * time.Millisecond)
	r := diagnostic.New()
	for _, id := range []string{"vm.endpoint", "vm.delivery", "vm.backlog"} {
		r.Register(id, true, 0, 0)
	}
	stats := &Stats{Diagnostics: r}
	var empty, batches atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	sender := &VMSender{Config: s.Config.Outputs.VictoriaMetrics, Client: &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		status := 204
		if len(b) == 0 {
			empty.Add(1)
		} else {
			batches.Add(1)
			if fail.Load() {
				status = 401
			}
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("do not log secret body"))}, nil
	})}}
	ctx, stop := context.WithTimeout(context.Background(), 4*time.Second)
	defer stop()
	done := make(chan struct{})
	go func() { defer close(done); (&VMWorker{s, sender, stats}).Run(ctx) }()
	for batches.Load() < 1 {
		if ctx.Err() != nil {
			t.Fatal("no batch")
		}
		time.Sleep(time.Millisecond)
	}
	for empty.Load() < 2 {
		if ctx.Err() != nil {
			t.Fatal("no repeat idle probe")
		}
		time.Sleep(time.Millisecond)
	}
	if r.Snapshot(time.Now()).Ready || stats.VMHealthy.Load() {
		t.Fatal("empty success hid failed real batch")
	}
	fail.Store(false)
	for stats.VMSent.Load() != 1 {
		if ctx.Err() != nil {
			t.Fatal("no recovery")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	<-done
	if scalar(t, s, "SELECT count(*) FROM vm_samples WHERE delivered=0") != 0 {
		t.Fatal("pending sample not delivered")
	}
}
func TestVMHTTPReasonAndRedaction(t *testing.T) {
	for _, code := range []int{401, 403, 404, 429, 500, 503} {
		s := &VMSender{Config: config.VM{ImportEndpoint: "https://example.invalid/import?secret=never-print"}, Client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("password=never-print"))}, nil
		})}}
		err := s.Send(context.Background(), nil)
		if err == nil || strings.Contains(err.Error(), "never-print") {
			t.Fatal(code, err)
		}
	}
}

func TestVMEndpointTLSAndTimeout(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	c := config.DefaultOutputs().VictoriaMetrics
	c.ImportEndpoint = server.URL + "/api/v1/import?secret=never-print"
	sender, err := NewVMSender(c)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	err = sender.Send(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "TLS certificate verification failed") || strings.Contains(err.Error(), "never-print") {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	if err = sender.Send(ctx, nil); err != context.Canceled {
		t.Fatal(err)
	}
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server2.Close()
	sender.Config.ImportEndpoint = server2.URL
	ctx, stop = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if err = sender.Send(ctx, nil); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
}

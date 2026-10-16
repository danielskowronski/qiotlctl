package diagnostic

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEvidenceGraceExpiryAndCopies(t *testing.T) {
	r := New()
	r.Register("vm", true, 30*time.Second, time.Minute)
	now := time.Unix(1000, 0)
	if r.Snapshot(now).Ready {
		t.Fatal("unattempted check passed")
	}
	r.SetAt("vm", Failed, "offline", "retry", nil, now)
	if r.Snapshot(now).Ready {
		t.Fatal("initial failure hidden by grace")
	}
	counts := map[string]int64{"pending": 5}
	r.SetAt("vm", OK, "delivered", "", counts, now)
	counts["pending"] = 99
	if r.Snapshot(now).Checks[0].Counts["pending"] != 5 {
		t.Fatal("aliased input")
	}
	r.SetAt("vm", Failed, "timeout", "retry", nil, now.Add(time.Second))
	if !r.Snapshot(now.Add(20 * time.Second)).Ready {
		t.Fatal("grace not honored")
	}
	r.SetAt("vm", Failed, "different failure", "retry", nil, now.Add(25*time.Second))
	if r.Snapshot(now.Add(31 * time.Second)).Ready {
		t.Fatal("changed reason reset failure deadline")
	}
	r.SetAt("vm", OK, "recovered", "", nil, now.Add(time.Minute))
	if !r.Snapshot(now.Add(time.Minute)).Ready || r.Snapshot(now.Add(3*time.Minute)).Ready {
		t.Fatal("recovery or evidence expiry wrong")
	}
	r.Register("disabled", false, 0, 0)
	r.SetAt("disabled", Disabled, "off", "", nil, now)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r.Set("concurrent", Warning, "waiting", "", counts)
				s := r.Snapshot(time.Now())
				for _, c := range s.Checks {
					if c.Counts != nil {
						c.Counts["pending"] = 10
					}
				}
			}
		}()
	}
	wg.Wait()
}
func TestReporterChangesAndReminder(t *testing.T) {
	var buf bytes.Buffer
	reporter := Reporter{Logger: slog.New(slog.NewJSONHandler(&buf, nil)), Interval: time.Minute}
	r := New()
	r.Register("ha", true, 0, 0)
	now := time.Unix(1000, 0)
	r.SetAt("ha", Failed, "subscribe denied for homeassistant/status", "allow subscribe", nil, now)
	reporter.Emit(r.Snapshot(now))
	reporter.Emit(r.Snapshot(now.Add(time.Second)))
	if strings.Count(buf.String(), "health check") != 1 {
		t.Fatal("unchanged failure spam", buf.String())
	}
	reporter.Emit(r.Snapshot(now.Add(time.Minute)))
	r.SetAt("ha", OK, "connected", "", nil, now.Add(61*time.Second))
	reporter.Emit(r.Snapshot(now.Add(61 * time.Second)))
	if strings.Count(buf.String(), "health check") != 2 || !strings.Contains(buf.String(), "health recovered") {
		t.Fatal(buf.String())
	}
}

func TestReporterSuppressesSuccessRefresh(t *testing.T) {
	var buf bytes.Buffer
	reporter := Reporter{Logger: slog.New(slog.NewJSONHandler(&buf, nil)), Interval: time.Minute}
	r := New()
	r.Set("vm.endpoint", OK, "empty probe accepted", "", nil)
	reporter.Emit(r.Snapshot(time.Now()))
	r.Set("vm.endpoint", OK, "real batch accepted", "", nil)
	reporter.Emit(r.Snapshot(time.Now().Add(time.Hour)))
	if strings.Count(buf.String(), "health check") != 1 || strings.Contains(buf.String(), "health recovered") {
		t.Fatal(buf.String())
	}
}

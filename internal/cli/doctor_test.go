package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/command"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
)

func TestDoctorDaemonSnapshot(t *testing.T) {
	for _, status := range []diagnostic.Status{diagnostic.OK, diagnostic.Failed, diagnostic.Unknown} {
		t.Run(string(status), func(t *testing.T) {
			r := diagnostic.New()
			r.Register("vm.endpoint", true, 0, 0)
			r.Set("vm.endpoint", status, "endpoint check", "check credentials", nil)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/healthz" || req.Method != "GET" {
					t.Error(req.Method, req.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(r.Snapshot(time.Now()))
			}))
			defer server.Close()
			out, err := invoke(t, "doctor", "--daemon-url", server.URL, "--format", "json")
			var got diagnostic.Snapshot
			if json.Unmarshal([]byte(out), &got) != nil || got.SchemaVersion != 1 {
				t.Fatal(out)
			}
			want := 0
			if status == diagnostic.Failed {
				want = 10
			}
			if status == diagnostic.Unknown {
				want = 11
			}
			if command.ExitCode(err) != want {
				t.Fatal(err, want)
			}
		})
	}
}

func TestDoctorRejectsMixedModeAndSecretURL(t *testing.T) {
	for _, args := range [][]string{
		{"doctor", "--daemon-url", "http://user:never-print@example.invalid"},
		{"doctor", "--daemon-url", "http://example.invalid?token=never-print"},
		{"doctor", "--daemon-url", "http://example.invalid", "--probe-publish"},
	} {
		out, err := invoke(t, args...)
		if err == nil || strings.Contains(out+err.Error(), "never-print") {
			t.Fatal(out, err)
		}
	}
}

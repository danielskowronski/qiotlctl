package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/command"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/doctor"
	"github.com/danielskowronski/qiotlctl/internal/transport"
	"github.com/spf13/cobra"
)

func (a *app) doctorCommand() *cobra.Command {
	var format, daemonURL string
	var timeout time.Duration
	var aliases []string
	var probe bool
	cmd := &cobra.Command{Use: "doctor", Short: "Diagnose MQTT permissions, HA discovery and VM delivery", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if timeout <= 0 || (format != "text" && format != "json") {
			return &command.Error{Code: 2, Message: "doctor requires a positive timeout and format text or json"}
		}
		ctx, stop := context.WithTimeout(cmd.Context(), timeout)
		defer stop()
		var s diagnostic.Snapshot
		if daemonURL != "" {
			if probe || len(aliases) > 0 || len(a.paths) > 0 {
				return &command.Error{Code: 2, Message: "--daemon-url inspects the daemon's complete effective configuration; do not combine with --config, --device or --probe-publish"}
			}
			var err error
			s, err = readDaemonHealth(ctx, daemonURL)
			if err != nil {
				return err
			}
		} else {
			c, err := a.load()
			if err == nil {
				err = selectDevices(&c, aliases)
			}
			if err != nil {
				r := diagnostic.New()
				r.Register("config", true, 0, 0)
				r.Set("config", diagnostic.Failed, err.Error(), "correct YAML, includes or environment references", nil)
				s = r.Snapshot(time.Now())
			} else {
				s = doctor.Run(ctx, c, probe)
			}
		}
		if format == "json" {
			if err := writeJSON(cmd.OutOrStdout(), s); err != nil {
				return err
			}
		} else {
			for _, c := range s.Checks {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%-8s %-22s %s\n", c.Status, c.ID, c.Reason); err != nil {
					return err
				}
				if c.Remedy != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "         action: %s\n", c.Remedy)
				}
				if len(c.Counts) > 0 {
					b, _ := json.Marshal(c.Counts)
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "         evidence: %s\n", b)
				}
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "ready=%t\n", s.Ready)
		}
		for _, c := range s.Checks {
			if c.Blocking && c.Status == diagnostic.Failed {
				return &command.Error{Code: 10, Message: "doctor found failed required checks"}
			}
		}
		if !s.Ready {
			return &command.Error{Code: 11, Message: "doctor could not verify all required checks"}
		}
		return nil
	}}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	cmd.Flags().StringVar(&daemonURL, "daemon-url", "", "Read the running daemon's /healthz snapshot")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Overall diagnostic deadline")
	cmd.Flags().StringSliceVar(&aliases, "device", nil, "Inspect selected device aliases")
	cmd.Flags().BoolVar(&probe, "probe-publish", false, "Publish a nonretained nonce only under <state_prefix>/diagnostics/<unique-id> and verify receipt")
	return cmd
}
func readDaemonHealth(ctx context.Context, address string) (diagnostic.Snapshot, error) {
	var s diagnostic.Snapshot
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return s, errors.New("daemon URL must be HTTP(S) without credentials, query or fragment")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return s, errors.New("invalid daemon URL")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return s, fmt.Errorf("daemon health: %s", transport.NetworkReason(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return s, fmt.Errorf("daemon health returned HTTP %d", resp.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s); err != nil || s.SchemaVersion != 1 || len(s.Checks) == 0 {
		return s, errors.New("invalid or unsupported daemon health snapshot")
	}
	return s, nil
}

package cli

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/command"
	"github.com/danielskowronski/qiotlctl/internal/protocol"
	"github.com/danielskowronski/qiotlctl/internal/settings"
	"github.com/spf13/cobra"
)

func ExitCode(err error) int { return command.ExitCode(err) }

type commandFlags struct {
	timeout                                    time.Duration
	dryRun, waitForUplink, requireConfirmation bool
	record, format                             string
}

func (f *commandFlags) register(cmd *cobra.Command) {
	cmd.Flags().DurationVar(&f.timeout, "timeout", 0, "Total operation deadline; defaults to commands.timeout (30s)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "Validate and show redacted plan without connecting or publishing")
	cmd.Flags().BoolVar(&f.waitForUplink, "wait-for-uplink", false, "Wait for fresh device traffic before sending, within the same deadline")
	cmd.Flags().BoolVar(&f.requireConfirmation, "require-confirmation", false, "Exit 7 if only publication can be established")
	cmd.Flags().StringVar(&f.record, "record", "", "SQLite journal destination; overrides storage.path; empty disables")
	cmd.Flags().StringVar(&f.format, "format", "json", "Result format: json or text; settings get also supports yaml")
}
func (a *app) runCommand(cmd *cobra.Command, alias string, req command.Request, f commandFlags) error {
	req.IgnoreOfficialLimits = a.settingsOptions.IgnoreOfficialLimits
	c, err := a.load()
	if err != nil {
		return &command.Error{Code: 2, Message: err.Error()}
	}
	d, err := command.FindDevice(c, alias)
	if err != nil {
		return &command.Error{Code: 2, Message: err.Error()}
	}
	if f.format != "json" && f.format != "text" && !(f.format == "yaml" && (req.Operation == "settings-get" || req.Operation == "settings-probe")) {
		return &command.Error{Code: 2, Message: "format must be json or text"}
	}
	if cmd.Flags().Changed("timeout") && f.timeout <= 0 {
		return &command.Error{Code: 2, Message: "timeout must be positive"}
	}
	plan, err := command.Build(d, req, time.Now().UTC())
	if err != nil {
		code := 2
		if errors.Is(err, command.ErrUnsupported) {
			code = 3
		}
		return &command.Error{Code: code, Message: err.Error()}
	}
	if f.dryRun {
		return writeJSON(cmd.OutOrStdout(), plan)
	}
	if cmd.Flags().Changed("record") {
		c.Storage.Path = f.record
	}
	if _, err = fmt.Fprintln(cmd.ErrOrStderr(), plan.Detail); err != nil {
		return err
	}
	result, runErr := command.Execute(cmd.Context(), c, d, req, command.Options{Timeout: f.timeout, WaitForUplink: f.waitForUplink, RequireConfirmation: f.requireConfirmation})
	if f.format == "yaml" {
		if result.Response != nil {
			err = writeYAML(cmd.OutOrStdout(), result.Response.SettingsConfig)
		}
	} else if f.format == "json" {
		err = writeJSON(cmd.OutOrStdout(), result)
	} else {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s - %s\n", result.Alias, result.Operation, result.Status, result.Evidence)
		if err == nil && result.Response != nil {
			err = writeJSON(cmd.OutOrStdout(), result.Response)
		}
	}
	if err != nil {
		return err
	}
	return runErr
}
func (a *app) settingsCommand() *cobra.Command {
	root := &cobra.Command{Use: "settings", Short: "Read fresh device settings"}
	var f commandFlags
	var reassert time.Duration
	get := &cobra.Command{Use: "get ALIAS", Short: "Request a fresh settings report", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := command.Request{Operation: "settings-get"}
		if cmd.Flags().Changed("reassert-upload-interval") {
			if reassert <= 0 && !a.settingsOptions.IgnoreOfficialLimits {
				return errors.New("reassert-upload-interval must be positive")
			}
			p, err := command.ProbePatch(reassert, a.settingsOptions)
			if err != nil {
				return err
			}
			req = command.Request{Operation: "settings-probe", Settings: &p, AwaitReport: true}
		}
		return a.runCommand(cmd, args[0], req, f)
	}}
	f.register(get)
	get.Flags().DurationVar(&reassert, "reassert-upload-interval", 0, "Explicit HEX write to trigger a report; use the known current interval, e.g. 5m")
	root.AddCommand(get, &cobra.Command{Use: "catalog", Short: "List human settings and their protocol mappings (encoding support, not firmware guarantees)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return writeJSON(cmd.OutOrStdout(), settings.Catalog) }})
	return root
}
func (a *app) commandCommand() *cobra.Command {
	var f commandFlags
	var req command.Request
	var file, representation string
	cmd := &cobra.Command{Use: "command ALIAS {realtime|sync-time|raw}", Short: "Send one explicit command and report its completion evidence", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		req.Operation = args[1]
		for _, name := range []string{"duration", "interval", "verify"} {
			if req.Operation != "realtime" && cmd.Flags().Changed(name) {
				return &command.Error{Code: 2, Message: "--" + name + " applies only to realtime"}
			}
		}
		for _, name := range []string{"file", "input-format", "wait-ack"} {
			if req.Operation != "raw" && cmd.Flags().Changed(name) {
				return &command.Error{Code: 2, Message: "--" + name + " applies only to raw"}
			}
		}
		if req.Operation == "raw" {
			b, err := input(cmd, file, nil, protocol.MaxPayload*3)
			if err != nil {
				return err
			}
			switch representation {
			case "raw":
			case "hex":
				b, err = hex.DecodeString(strings.Join(strings.Fields(string(b)), ""))
			case "base64":
				b, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
			default:
				return &command.Error{Code: 2, Message: "input-format must be raw, hex or base64"}
			}
			if err != nil {
				return &command.Error{Code: 2, Message: "invalid raw input representation"}
			}
			req.Payload = b
		}
		return a.runCommand(cmd, args[0], req, f)
	}}
	f.register(cmd)
	cmd.Flags().DurationVar(&req.Duration, "duration", 5*time.Minute, "Realtime duration: 1m..1h in integral seconds")
	cmd.Flags().DurationVar(&req.Interval, "interval", 5*time.Second, "Realtime reporting interval: integral seconds greater than 2s")
	cmd.Flags().BoolVar(&req.Verify, "verify", false, "For realtime, wait for two distinct fresh realtime samples (uncorrelated evidence)")
	cmd.Flags().BoolVar(&req.WaitACK, "wait-ack", false, "For raw JSON type 17, wait for matching ack_id/code; payload must supply a unique integer id")
	cmd.Flags().StringVar(&file, "file", "-", "Raw payload file, or - for stdin")
	cmd.Flags().StringVar(&representation, "input-format", "raw", "Payload representation: raw, hex or base64")
	return cmd
}

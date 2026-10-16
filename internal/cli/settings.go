package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/command"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/protocol"
	"github.com/danielskowronski/qiotlctl/internal/settings"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type settingsFlags struct {
	fromConfig           bool
	file                 string
	set, rawHEX, rawJSON []string
}

func (f *settingsFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.fromConfig, "from-config", false, "Use the target device's configured desired settings")
	cmd.Flags().StringVar(&f.file, "settings-file", "", "YAML settings mapping (no devices wrapper); - for stdin")
	cmd.Flags().StringArrayVar(&f.set, "set", nil, "Human setting path=value; repeat; overrides config/file values")
	cmd.Flags().StringArrayVar(&f.rawHEX, "raw-hex", nil, "Raw HEX property 0xKEY=HEXVALUE; repeat")
	cmd.Flags().StringArrayVar(&f.rawJSON, "raw-json", nil, "Raw JSON settings object; repeat; preserves JSON value types")
}
func (f settingsFlags) values(cmd *cobra.Command, base settings.Values) (settings.Values, error) {
	v := settings.Clone(base)
	if f.file != "" {
		data, e := input(cmd, f.file, nil, protocol.MaxPayload)
		if e != nil {
			return nil, e
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		var more settings.Values
		if e = dec.Decode(&more); e != nil {
			return nil, e
		}
		var trailing any
		if dec.Decode(&trailing) != io.EOF {
			return nil, errors.New("settings-file must contain exactly one YAML document")
		}
		if e = mergeSettings(v, more); e != nil {
			return nil, e
		}
	}
	for _, assignment := range f.set {
		if strings.HasPrefix(assignment, "raw.") {
			return nil, errors.New("use --raw-hex or --raw-json for raw values")
		}
		if e := settings.ParseSet(v, assignment); e != nil {
			return nil, e
		}
	}
	if len(f.rawHEX)+len(f.rawJSON) > 0 {
		raw, ok := v["raw"].(map[string]any)
		if !ok {
			raw = map[string]any{}
			v["raw"] = raw
		}
		list, _ := raw["hex"].([]any)
		for _, entry := range f.rawHEX {
			k, b, ok := strings.Cut(entry, "=")
			if !ok {
				return nil, errors.New("--raw-hex requires 0xKEY=HEXVALUE")
			}
			list = append(list, map[string]any{"key": k, "value_hex": b})
		}
		if len(f.rawHEX) > 0 {
			raw["hex"] = list
		}
		object, _ := raw["json"].(map[string]any)
		if object == nil {
			object = map[string]any{}
		}
		for _, text := range f.rawJSON {
			msg, e := protocol.Decode([]byte(text), "json")
			if e != nil {
				return nil, e
			}
			for key, b := range msg.Fields {
				if _, exists := object[key]; exists {
					return nil, fmt.Errorf("duplicate raw JSON key %s", key)
				}
				var value any
				d := json.NewDecoder(bytes.NewReader(b))
				d.UseNumber()
				if e = d.Decode(&value); e != nil {
					return nil, e
				}
				object[key] = value
			}
		}
		if len(f.rawJSON) > 0 {
			raw["json"] = object
		}
	}
	return v, nil
}

// File overlays have the same recursive-map/list-replacement/null-removal policy
// as config. Reuse the existing merge library rather than a second merge engine.
func mergeSettings(dst, src settings.Values) error { return settings.Merge(dst, src) }
func (a *app) applySettingsCommand() *cobra.Command {
	var f commandFlags
	var inputFlags settingsFlags
	var single, force, readback, check bool
	var probe time.Duration
	cmd := &cobra.Command{Use: "apply-settings ALIAS", Short: "Apply explicit desired settings and report evidence for each property", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := a.load()
		if err != nil {
			return err
		}
		d, err := command.FindDevice(c, args[0])
		if err != nil {
			return err
		}
		base := settings.Values{}
		if inputFlags.fromConfig {
			base = d.Settings
		}
		v, err := inputFlags.values(cmd, base)
		if err != nil {
			return err
		}
		kind := d.Protocol
		if kind == "auto" {
			kind = device.Lookup(d.Model).Protocol
		}
		patch, err := settings.Build(kind, v, a.settingsOptions)
		if err != nil {
			return &command.Error{Code: 2, Message: err.Error()}
		}
		if f.format != "json" && f.format != "text" {
			return errors.New("format must be json or text")
		}
		if cmd.Flags().Changed("timeout") && f.timeout <= 0 {
			return errors.New("timeout must be positive")
		}
		if cmd.Flags().Changed("readback-upload-interval") && probe <= 0 && !a.settingsOptions.IgnoreOfficialLimits {
			return errors.New("readback-upload-interval must be positive")
		}
		opt := command.ApplyOptions{Options: command.Options{Timeout: f.timeout, WaitForUplink: f.waitForUplink, RequireConfirmation: f.requireConfirmation}, SingleProperty: single, Force: force, Readback: readback, ReadbackUpload: probe}
		opt.SettingsOptions = a.settingsOptions
		opt.ReadbackUploadSet = cmd.Flags().Changed("readback-upload-interval")
		if _, err = command.Build(d, command.Request{Operation: "settings-apply", Settings: &patch}, time.Now().UTC()); err != nil {
			return &command.Error{Code: 2, Message: err.Error()}
		}
		if check {
			err = command.ValidateCheck(patch, opt)
		} else {
			err = command.ValidateApply(patch, opt)
		}
		if err != nil {
			var coded *command.Error
			if errors.As(err, &coded) {
				return err
			}
			return &command.Error{Code: 2, Message: err.Error()}
		}
		if check && (f.dryRun || force || single) {
			return errors.New("--check cannot be combined with --dry-run, --force or --single-property")
		}
		if f.dryRun {
			return writeJSON(cmd.OutOrStdout(), map[string]any{"alias": d.Alias, "settings": patch.Redacted(), "single_property": single, "query_before_write": kind == "json" && !force, "json_readback": readback && kind == "json", "hex_readback_upload_interval": probe.String(), "hex_readback_upload_requested": opt.ReadbackUploadSet, "ignore_official_limits": a.settingsOptions.IgnoreOfficialLimits, "automatic_replies": false, "note": "Offline plan; device support is unverified unless documented in SETTINGS.md. Raw values omitted; use offline encode to inspect bytes."})
		}
		if cmd.Flags().Changed("record") {
			c.Storage.Path = f.record
		}
		var result command.ApplyResult
		var runErr error
		if check {
			result, runErr = command.Check(cmd.Context(), c, d, patch, opt)
		} else {
			result, runErr = command.Apply(cmd.Context(), c, d, patch, opt)
		}
		if f.format == "json" {
			err = writeJSON(cmd.OutOrStdout(), result)
		} else {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", result.Alias, result.Status)
			for _, field := range result.Fields {
				if err != nil {
					break
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", field.Name, field.Status)
			}
		}
		if err != nil {
			return err
		}
		return runErr
	}}
	f.register(cmd)
	inputFlags.register(cmd)
	cmd.Flags().BoolVar(&check, "check", false, "Compare fresh settings without applying the patch; HEX requires an explicit readback upload write")
	cmd.Flags().BoolVar(&single, "single-property", false, "Send one wire property per operation; compound alarms/ranges stay atomic")
	cmd.Flags().BoolVar(&force, "force", false, "Reassert typed settings even if JSON readback already matches; raw properties always send")
	cmd.Flags().BoolVar(&readback, "readback", true, "Query JSON settings after each write; HEX needs a report or explicit upload probe")
	cmd.Flags().DurationVar(&probe, "readback-upload-interval", 0, "Explicit HEX write after silent settings to request readback, e.g. 5m; use known/current interval")
	return cmd
}
func (a *app) encodeSettingsCommand() *cobra.Command {
	var f settingsFlags
	var model, kind, format string
	var upload, record time.Duration
	var minimum, maximum int
	cmd := &cobra.Command{Use: "settings [ALIAS]", Short: "Encode human or raw settings offline using the same planner as apply-settings", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		base := settings.Values{}
		if len(args) > 0 {
			c, e := a.load()
			if e != nil {
				return e
			}
			d, e := command.FindDevice(c, args[0])
			if e != nil {
				return e
			}
			if model != "" && model != d.Model {
				return errors.New("model conflicts with configured device")
			}
			model = d.Model
			if kind != "" && kind != d.Protocol {
				return errors.New("protocol conflicts with configured device")
			}
			kind = d.Protocol
			if f.fromConfig {
				base = d.Settings
			}
		} else if f.fromConfig {
			return errors.New("--from-config needs a device alias")
		}
		inferred := device.Lookup(model).Protocol
		if kind == "" || kind == "auto" {
			kind = inferred
		}
		if inferred != "auto" && kind != inferred {
			return errors.New("model/protocol conflict")
		}
		v, e := f.values(cmd, base)
		if e != nil {
			return e
		}
		if cmd.Flags().Changed("upload-interval") {
			_ = settings.Set(v, "reporting.upload_interval", upload.String())
		}
		if cmd.Flags().Changed("record-interval") {
			_ = settings.Set(v, "reporting.record_interval", record.String())
		}
		if cmd.Flags().Changed("co2-min") || cmd.Flags().Changed("co2-max") {
			if !cmd.Flags().Changed("co2-min") || !cmd.Flags().Changed("co2-max") {
				return errors.New("both CO2 endpoints are required")
			}
			_ = settings.Set(v, "co2.indicator", map[string]any{"yellow_min_ppm": minimum, "yellow_max_ppm": maximum})
		}
		patch, e := settings.Build(kind, v, a.settingsOptions)
		if e != nil {
			return e
		}
		id, e := command.NewRequestID()
		if e != nil {
			return e
		}
		payload, e := patch.Encode(id, time.Now().UTC())
		if e != nil {
			return e
		}
		return outputPayload(cmd, payload, format)
	}}
	f.register(cmd)
	cmd.Flags().StringVar(&model, "model", "", "Optional model, e.g. CGP22C or CGDN1")
	cmd.Flags().StringVar(&kind, "protocol", "", "hex or json; inferred from a model/alias")
	cmd.Flags().DurationVar(&upload, "upload-interval", 0, "Upload interval; shorthand for --set reporting.upload_interval")
	cmd.Flags().DurationVar(&record, "record-interval", 0, "Record interval; shorthand for --set reporting.record_interval")
	cmd.Flags().IntVar(&minimum, "co2-min", 0, "Inclusive yellow CO2 lower bound, ppm")
	cmd.Flags().IntVar(&maximum, "co2-max", 0, "Inclusive yellow CO2 upper bound, ppm")
	cmd.Flags().StringVar(&format, "output-format", "hex", "hex, base64, raw or json")
	return cmd
}

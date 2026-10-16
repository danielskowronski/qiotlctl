package command

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/settings"
)

type ApplyOptions struct {
	Options
	SingleProperty    bool
	Force             bool
	Readback          bool
	ReadbackUpload    time.Duration
	ReadbackUploadSet bool // distinguishes an explicit experimental 0s from no probe
	SettingsOptions   settings.Options
}

func (o ApplyOptions) hasReadbackUpload() bool { return o.ReadbackUploadSet || o.ReadbackUpload != 0 }

type ApplyResult struct {
	Alias      string                 `json:"alias"`
	Status     string                 `json:"status"`
	Fields     []settings.Comparison  `json:"fields"`
	Operations []domain.CommandResult `json:"operations"`
}

func NewRequestID() (int64, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint32(b[:]) & 0x7fffffff), nil
}
func ProbePatch(interval time.Duration, options ...settings.Options) (settings.Patch, error) {
	return settings.Build("hex", settings.Values{"reporting": map[string]any{"upload_interval": interval.String()}}, options...)
}

// Apply serializes the complete multi-session transaction within this process.
// Each operation is independently journaled. A timeout never causes a write retry.
func Apply(ctx context.Context, c config.Config, d config.Device, p settings.Patch, opt ApplyOptions) (ApplyResult, error) {
	result := ApplyResult{Alias: d.Alias, Status: "prepared", Fields: p.Compare(nil), Operations: []domain.CommandResult{}}
	if err := ValidateApply(p, opt); err != nil {
		return result, &Error{2, err.Error()}
	}
	for i := range result.Fields {
		result.Fields[i].Status = "not_sent"
	}
	mutex, _ := localLocks.LoadOrStore(c.MQTT.URL+"\x00"+d.Topics.Telemetry, &sync.Mutex{})
	lock := mutex.(*sync.Mutex)
	if !lock.TryLock() {
		return result, &Error{8, "another operation for this device is active in this process"}
	}
	defer lock.Unlock()
	options := opt.Options
	options.lockHeld = true
	options.RequireConfirmation = false
	run := func(req Request, wait bool) (domain.CommandResult, error) {
		o := options
		o.WaitForUplink = wait
		r, e := Execute(ctx, c, d, req, o)
		result.Operations = append(result.Operations, r)
		return r, e
	}
	// Only JSON has a proven query. An explicit HEX probe is done after writes,
	// never disguised as a read-only preflight or as a no-op.
	if p.Protocol == "json" && !opt.Force {
		baseline, e := run(Request{Operation: "settings-get"}, false)
		if e != nil {
			result.Status = "failed"
			return result, e
		}
		if baseline.Response != nil {
			cmp := p.Compare(baseline.Response.SettingsObserved)
			remaining := settings.Patch{Protocol: p.Protocol}
			for i, f := range p.Fields {
				if cmp[i].Status == "matched" && !f.Raw {
					result.Fields[i] = cmp[i]
					result.Fields[i].Status = "unchanged"
				} else {
					remaining.Fields = append(remaining.Fields, f)
				}
			}
			p = remaining
		}
	}
	if len(p.Fields) == 0 {
		result.Status = "unchanged"
		return result, nil
	}
	groups := []settings.Patch{p}
	if opt.SingleProperty {
		groups = nil
		for _, f := range p.Fields {
			groups = append(groups, settings.Patch{Protocol: p.Protocol, Fields: []settings.Field{f}})
		}
	}
	update := func(group settings.Patch, obs settings.Observation, fallback string) {
		cmp := group.Compare(obs)
		for _, v := range cmp {
			if v.Status == "missing" {
				v.Status = fallback
			}
			for i := range result.Fields {
				if result.Fields[i].Key == v.Key {
					result.Fields[i] = v
				}
			}
		}
	}
	for _, group := range groups {
		id, e := NewRequestID()
		if e != nil {
			return result, e
		}
		r, e := run(Request{Operation: "settings-apply", Settings: &group, RequestID: id}, opt.WaitForUplink)
		fallback := "published_unverified"
		if r.Status == "acknowledged" {
			fallback = "acknowledged_unverified"
		}
		var obs settings.Observation
		if r.Response != nil {
			obs = r.Response.SettingsObserved
		}
		if e != nil {
			fallback = "unknown"
			if r.AttemptedAt == nil {
				fallback = "not_sent"
			}
			if r.Status == "rejected" {
				fallback = "rejected"
			}
			update(group, obs, fallback)
			result.Status = r.Status
			return result, e
		}
		if opt.Readback && p.Protocol == "json" {
			report, queryErr := run(Request{Operation: "settings-get"}, false)
			if queryErr != nil {
				update(group, obs, fallback)
				result.Status = "partial"
				return result, queryErr
			}
			obs = report.Response.SettingsObserved
		}
		if opt.hasReadbackUpload() && !group.HasHEX(0x04) {
			probe, _ := ProbePatch(opt.ReadbackUpload, opt.SettingsOptions)
			report, queryErr := run(Request{Operation: "settings-probe", Settings: &probe, AwaitReport: true}, opt.WaitForUplink)
			if queryErr != nil {
				update(group, obs, fallback)
				result.Status = "partial"
				return result, queryErr
			}
			obs = report.Response.SettingsObserved
		}
		update(group, obs, fallback)
	}
	result.Status = "verified"
	for _, f := range result.Fields {
		if f.Status != "matched" && f.Status != "unchanged" {
			result.Status = "partial"
			break
		}
	}
	if result.Status == "partial" && opt.RequireConfirmation {
		return result, &Error{7, "some settings lack matching readback; see per-field results"}
	}
	return result, nil
}

// ValidateApply checks the extra write used for HEX readback before any connection.
func ValidateApply(p settings.Patch, opt ApplyOptions) error {
	if len(p.Fields) == 0 {
		return errors.New("no settings supplied")
	}
	if opt.hasReadbackUpload() {
		if p.Protocol != "hex" {
			return errors.New("readback-upload-interval applies only to HEX")
		}
		probe, err := ProbePatch(opt.ReadbackUpload, opt.SettingsOptions)
		if err != nil {
			return err
		}
		for _, f := range p.Fields {
			if f.Key == "0x04" && string(f.HEX.Value) != string(probe.Fields[0].HEX.Value) {
				return errors.New("readback upload interval conflicts with requested upload interval")
			}
		}
	}
	return nil
}

// Check compares a fresh report with a patch without applying that patch. HEX
// still requires an explicitly authorized upload-interval write for the report.
func Check(ctx context.Context, c config.Config, d config.Device, p settings.Patch, opt ApplyOptions) (ApplyResult, error) {
	result := ApplyResult{Alias: d.Alias, Status: "unknown", Fields: p.Compare(nil), Operations: []domain.CommandResult{}}
	if err := ValidateCheck(p, opt); err != nil {
		var coded *Error
		if errors.As(err, &coded) {
			return result, err
		}
		return result, &Error{2, err.Error()}
	}
	req := Request{Operation: "settings-get"}
	if p.Protocol == "hex" {
		probe, _ := ProbePatch(opt.ReadbackUpload, opt.SettingsOptions)
		req = Request{Operation: "settings-probe", Settings: &probe, AwaitReport: true}
	}
	r, err := Execute(ctx, c, d, req, opt.Options)
	result.Operations = append(result.Operations, r)
	if err != nil {
		return result, err
	}
	result.Fields = p.Compare(r.Response.SettingsObserved)
	result.Status = "in_sync"
	for _, f := range result.Fields {
		if f.Status != "matched" {
			result.Status = "drift_or_unknown"
			break
		}
	}
	if result.Status != "in_sync" {
		return result, &Error{7, "desired settings differ or are absent/ambiguous in readback"}
	}
	return result, nil
}

func ValidateCheck(p settings.Patch, opt ApplyOptions) error {
	if len(p.Fields) == 0 {
		return errors.New("no settings supplied")
	}
	if p.Protocol == "hex" {
		if !opt.hasReadbackUpload() {
			return &Error{3, "HEX check requires an explicit --readback-upload-interval write; there is no verified read-only query"}
		}
		_, err := ProbePatch(opt.ReadbackUpload, opt.SettingsOptions)
		return err
	}
	if opt.hasReadbackUpload() {
		return errors.New("readback-upload-interval applies only to HEX")
	}
	return nil
}

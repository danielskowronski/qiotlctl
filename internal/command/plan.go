// Package command plans and executes one-shot, model-aware MQTT operations.
package command

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/protocol"
	"github.com/danielskowronski/qiotlctl/internal/settings"
)

var ErrUnsupported = errors.New("operation unsupported by device profile")

type Request struct {
	Operation            string
	Settings             *settings.Patch
	RequestID            int64
	AwaitReport          bool
	Duration             time.Duration
	Interval             time.Duration
	Payload              []byte
	WaitACK              bool
	Verify               bool
	IgnoreOfficialLimits bool
}

type Plan struct {
	Operation            string       `json:"operation"`
	Topic                string       `json:"topic"`
	Protocol             string       `json:"protocol"`
	Completion           string       `json:"completion"`
	Detail               string       `json:"detail"`
	RequestID            *int64       `json:"request_id,omitempty"`
	PayloadSHA256        string       `json:"payload_sha256"`
	PayloadSize          int          `json:"payload_size"`
	Preview              domain.Event `json:"preview"`
	Payload              []byte       `json:"-"`
	IgnoreOfficialLimits bool         `json:"ignore_official_limits,omitempty"`
}

func Build(d config.Device, req Request, now time.Time) (Plan, error) {
	p := Plan{Operation: req.Operation, Topic: d.Topics.Commands, Protocol: d.Protocol, Completion: "published"}
	p.IgnoreOfficialLimits = req.IgnoreOfficialLimits
	if !d.Active() {
		return p, errors.New("device is disabled")
	}
	if d.Topics.Commands == "" {
		return p, errors.New("device has no command topic")
	}
	profile := device.Lookup(d.Model)
	if p.Protocol == "auto" || p.Protocol == "" {
		p.Protocol = profile.Protocol
	}
	if req.WaitACK && req.Operation != "raw" {
		return p, errors.New("--wait-ack is supported only for raw JSON type-17 requests with an ID")
	}
	if req.Verify && req.Operation != "realtime" {
		return p, errors.New("--verify applies only to realtime; settings get always awaits a fresh report")
	}
	var err error
	switch req.Operation {
	case "settings-apply", "settings-probe":
		if req.Settings == nil || req.Settings.Protocol != p.Protocol {
			return p, errors.New("settings patch protocol mismatch")
		}
		p.Payload, err = req.Settings.Encode(req.RequestID, now)
		p.Detail = "settings publication; silent writes do not prove acceptance or rejection"
		if p.Protocol == "json" {
			id := req.RequestID
			p.RequestID = &id
			p.Completion = "settings-write"
			p.Detail = "await matching JSON acknowledgement or fresh settings report; verify field values separately"
		}
		if req.AwaitReport || req.Settings.HasHEX(0x04) {
			p.Completion = "settings-report"
			p.Detail = "await fresh settings report without request correlation; compare individual fields separately"
		}
	case "settings-get":
		p.Completion = "settings-report"
		p.Detail = "fresh settings report; this protocol has no request correlation ID"
		switch p.Protocol {
		case "json":
			p.Payload = []byte(`{"type":"28"}`)
		case "hex":
			return p, fmt.Errorf("%w: HEX has no verified settings query; use watch to inspect 0x39 reports", ErrUnsupported)
		default:
			return p, ErrUnsupported
		}
	case "sync-time":
		if now.Unix() < 1577836800 || now.Unix() > 4294967295 {
			return p, errors.New("host clock is outside supported 2020..2106 range")
		}
		p.Detail = "no documented device acknowledgement; publication does not prove clock adjustment"
		switch profile.Model {
		case "CGDN1":
			p.Payload, err = json.Marshal(map[string]any{"type": "18", "timestamp": now.Unix()})
			p.Detail += "; observed type-18 compatibility workaround"
		case "CGP22C":
			value := make([]byte, 4)
			binary.LittleEndian.PutUint32(value, uint32(now.Unix()))
			p.Payload, err = protocol.EncodeHEX(0x32, []protocol.TLV{{Key: 0x15, Value: value}})
		default:
			return p, ErrUnsupported
		}
	case "realtime":
		if profile.Model != "CGDN1" {
			return p, ErrUnsupported
		}
		for _, v := range []time.Duration{req.Duration, req.Interval} {
			if v%time.Second != 0 || v/time.Second < -2147483648 || v/time.Second > 2147483647 {
				return p, errors.New("realtime values must be whole seconds fitting a signed 32-bit integer")
			}
		}
		if !req.IgnoreOfficialLimits && (req.Duration < time.Minute || req.Duration > time.Hour) {
			return p, errors.New("realtime duration must be integral seconds in 1m..1h")
		}
		if !req.IgnoreOfficialLimits && (req.Interval <= 2*time.Second || req.Interval > req.Duration) {
			return p, errors.New("realtime interval must be integral seconds, greater than 2s and no longer than duration")
		}
		p.Payload, err = json.Marshal(map[string]string{"type": "12", "up_itvl": strconv.FormatInt(int64(req.Interval/time.Second), 10), "duration": strconv.FormatInt(int64(req.Duration/time.Second), 10)})
		p.Detail = "no documented acknowledgement; duration ends automatically"
		if req.IgnoreOfficialLimits {
			p.Detail = "experimental realtime request; no documented acknowledgement or guarantee that the requested duration is honored"
		}
		if req.Verify {
			p.Completion = "realtime-observation"
			p.Detail = "await two distinct fresh realtime samples; observation cannot prove which controller caused them"
		}
	case "raw":
		m, e := protocol.Decode(req.Payload, p.Protocol)
		if e != nil {
			return p, e
		}
		p.Protocol = m.Protocol
		p.Payload = append([]byte(nil), req.Payload...)
		p.Detail = "exact payload bytes; no device execution confirmation"
		if req.WaitACK {
			id, valid := device.JSONInteger(m.Fields["id"])
			if m.Protocol != "json" || wireType(m) != "17" || !valid || id < 0 {
				return p, errors.New("--wait-ack requires a JSON type-17 payload with a nonnegative integer id; use a unique ID for each request")
			}
			p.Completion = "json-ack"
			p.RequestID = &id
			p.Detail = "await type-18 ack_id and code; acceptance is not settings readback"
		}
	default:
		return p, errors.New("unknown operation; choose realtime, sync-time or raw")
	}
	if err != nil {
		return p, err
	}
	p.Preview, err = device.Decode(p.Payload, p.Protocol, d.Model, domain.ToDevice, now)
	if err != nil {
		return p, err
	}
	p.Preview.DeviceID, p.Preview.Alias, p.Preview.Topic = d.ID, d.Alias, p.Topic
	sum := sha256.Sum256(p.Payload)
	p.PayloadSHA256 = hex.EncodeToString(sum[:])
	p.PayloadSize = len(p.Payload)
	return p, nil
}

func wireType(m protocol.Message) string {
	var s string
	if json.Unmarshal(m.Fields["type"], &s) == nil {
		return s
	}
	n, ok := device.JSONInteger(m.Fields["type"])
	if ok {
		return strconv.FormatInt(n, 10)
	}
	return ""
}

// Matcher is owned by a single operation. It is installed before PUBLISH, and
// never treats an MQTT PUBACK as a device acknowledgement.
type Matcher struct {
	Plan     Plan
	Device   config.Device
	SentAt   time.Time
	realtime map[int64]bool
}

func (m *Matcher) Match(c domain.Capture, e domain.Event) (status, evidence string) {
	if c.Topic != m.Device.Topics.Telemetry || c.Direction != domain.FromDevice || c.ReceivedAt.Before(m.SentAt) || e.Error != "" || (c.Retained != nil && *c.Retained) || (c.Duplicate != nil && *c.Duplicate) {
		return "", ""
	}
	msg, err := protocol.Decode(c.Payload, m.Plan.Protocol)
	if err != nil || !device.IdentityMatches(msg, m.Device.MAC) {
		return "", ""
	}
	if (m.Plan.Completion == "settings-write" || m.Plan.Completion == "settings-report") && e.ReportedAt != nil && *e.ReportedAt >= 1577836800 && *e.ReportedAt < m.SentAt.Unix()-5 {
		return "", ""
	}
	switch m.Plan.Completion {
	case "json-ack", "settings-write":
		if m.Plan.Completion == "settings-write" && wireType(msg) == "28" && len(e.Settings) > 0 {
			return "reported", "fresh uncorrelated settings report; field comparison required"
		}
		id, valid := device.JSONInteger(msg.Fields["ack_id"])
		code, codeOK := device.JSONInteger(msg.Fields["code"])
		if wireType(msg) != "18" || !valid || !codeOK || m.Plan.RequestID == nil || id != *m.Plan.RequestID {
			return "", ""
		}
		if code != 0 {
			return "rejected", fmt.Sprintf("matching ack_id returned code %d", code)
		}
		return "acknowledged", "matching ack_id returned code 0"
	case "settings-report":
		expected := (msg.Protocol == "hex" && msg.Command == 0x39) || (msg.Protocol == "json" && wireType(msg) == "28")
		if !expected || len(e.Settings) == 0 {
			return "", ""
		}
		// Epoch clocks cannot establish age. Valid old timestamps, when present,
		// positively identify stale reports and are rejected.
		if e.ReportedAt != nil && *e.ReportedAt >= 1577836800 && *e.ReportedAt < m.SentAt.Unix()-5 {
			return "", ""
		}
		return "verified", "fresh settings report on target uplink; uncorrelated with request"
	case "realtime-observation":
		if wireType(msg) != "12" {
			return "", ""
		}
		if m.realtime == nil {
			m.realtime = map[int64]bool{}
		}
		for _, s := range e.Samples {
			if s.TimeQuality == "device" && s.Timestamp >= m.SentAt.Unix()-2 && s.Timestamp <= c.ReceivedAt.Unix()+5 && len(s.Readings) > 0 {
				m.realtime[s.Timestamp] = true
			}
		}
		if len(m.realtime) >= 2 {
			return "verified", "two distinct fresh realtime samples observed; causality is uncorrelated"
		}
	}
	return "", ""
}

func FindDevice(c config.Config, alias string) (config.Device, error) {
	for key, d := range c.Devices {
		if strings.EqualFold(key, alias) {
			return d, nil
		}
	}
	return config.Device{}, fmt.Errorf("unknown device alias %q", alias)
}

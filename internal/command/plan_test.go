package command

import (
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/protocol"
)

func fixture(model string) config.Device {
	return config.Device{ID: "fixture", Alias: "F", Model: model, Protocol: device.Lookup(model).Protocol, MAC: "AA:BB:CC:DD:EE:FF", Topics: config.Topics{Telemetry: "fixture/up", Commands: "fixture/down"}}
}
func TestPlans(t *testing.T) {
	now := time.Unix(1792000000, 0)
	for _, tc := range []struct {
		model   string
		req     Request
		payload string
	}{
		{"CGDN1", Request{Operation: "settings-get"}, `{"type":"28"}`},

		{"CGDN1", Request{Operation: "sync-time"}, `{"timestamp":1792000000,"type":"18"}`},
		{"CGDN1", Request{Operation: "realtime", Duration: time.Minute, Interval: 3 * time.Second}, `{"duration":"60","type":"12","up_itvl":"3"}`},
	} {
		p, err := Build(fixture(tc.model), tc.req, now)
		if err != nil {
			t.Fatal(err)
		}
		payload := string(p.Payload)
		if tc.model == "CGP22C" {
			payload = hex.EncodeToString(p.Payload)
		}
		if payload != tc.payload {
			t.Fatalf("%s: %s", tc.req.Operation, payload)
		}
	}
	for _, req := range []Request{{Operation: "realtime", Duration: 59 * time.Second, Interval: 3 * time.Second}, {Operation: "realtime", Duration: time.Minute, Interval: 2 * time.Second}, {Operation: "raw", Payload: []byte(`{"type":"17","id":1.5}`), WaitACK: true}, {Operation: "raw", Payload: []byte(`{"type":"28","id":1}`), WaitACK: true}} {
		if _, err := Build(fixture("CGDN1"), req, now); err == nil {
			t.Fatal("accepted invalid request", req)
		}
	}
	if _, err := Build(fixture("CGP22C"), Request{Operation: "realtime", Duration: time.Minute, Interval: 3 * time.Second}, now); err != ErrUnsupported {
		t.Fatal(err)
	}
	raw := []byte("{ \"type\": \"17\", \"id\": 123, \"setting\": {\"secret\":\"do-not-log\"} }\n")
	p, err := Build(fixture("CGDN1"), Request{Operation: "raw", Payload: raw, WaitACK: true}, now)
	if err != nil || string(p.Payload) != string(raw) || p.Preview.Settings["secret"] != "[redacted]" {
		t.Fatal(p, err)
	}
}

func TestRealtimeIgnoreOfficialLimits(t *testing.T) {
	for _, tc := range []struct {
		duration, interval time.Duration
		want               string
	}{
		{30 * time.Second, time.Second, `{"duration":"30","type":"12","up_itvl":"1"}`},
		{2 * time.Hour, 3 * time.Hour, `{"duration":"7200","type":"12","up_itvl":"10800"}`},
		{0, -time.Second, `{"duration":"0","type":"12","up_itvl":"-1"}`},
		{2147483647 * time.Second, -2147483648 * time.Second, `{"duration":"2147483647","type":"12","up_itvl":"-2147483648"}`},
	} {
		req := Request{Operation: "realtime", Duration: tc.duration, Interval: tc.interval}
		if _, err := Build(fixture("CGDN1"), req, time.Now()); err == nil {
			t.Fatal("accepted without opt-in", tc)
		}
		req.IgnoreOfficialLimits = true
		p, err := Build(fixture("CGDN1"), req, time.Now())
		if err != nil || string(p.Payload) != tc.want {
			t.Fatal(string(p.Payload), err)
		}
	}
	for _, v := range []time.Duration{1500 * time.Millisecond, 2147483648 * time.Second, -2147483649 * time.Second} {
		for _, req := range []Request{
			{Operation: "realtime", Duration: v, Interval: time.Second, IgnoreOfficialLimits: true},
			{Operation: "realtime", Duration: time.Minute, Interval: v, IgnoreOfficialLimits: true},
		} {
			if _, err := Build(fixture("CGDN1"), req, time.Now()); err == nil {
				t.Fatal("invalid wire value accepted", req)
			}
		}
	}
}
func TestMatchingEvidence(t *testing.T) {
	now := time.Now().UTC()
	d := fixture("CGDN1")
	p, err := Build(d, Request{Operation: "raw", Payload: []byte(`{"type":"17","id":2147483001,"setting":{}}`), WaitACK: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		payload                     string
		topic                       string
		retained, duplicate, before bool
		want                        string
	}{
		{`{"type":"18","ack_id":2147483001,"code":0}`, "fixture/up", false, false, false, "acknowledged"},
		{`{"type":"18","ack_id":2147483001,"code":2}`, "fixture/up", false, false, false, "rejected"},
		{`{"type":"18","ack_id":2147483002,"code":0}`, "fixture/up", false, false, false, ""},
		{`{"type":"18","ack_id":2147483001,"code":0,"mac":"other"}`, "fixture/up", false, false, false, ""},
		{`{"type":"18","ack_id":2147483001,"code":0}`, "other/up", false, false, false, ""},
		{`{"type":"18","ack_id":2147483001,"code":0}`, "fixture/up", true, false, false, ""},
		{`{"type":"18","ack_id":2147483001,"code":0}`, "fixture/up", false, true, false, ""},
		{`{"type":"18","ack_id":2147483001,"code":0}`, "fixture/up", false, false, true, ""},
		{`{"type":"18","ack_id":2147483001}`, "fixture/up", false, false, false, ""},
	} {
		received := now.Add(time.Second)
		if tc.before {
			received = now.Add(-time.Second)
		}
		cap := domain.Capture{Topic: tc.topic, Payload: []byte(tc.payload), Direction: domain.FromDevice, ReceivedAt: received, Retained: &tc.retained, Duplicate: &tc.duplicate}
		event, _ := device.Decode(cap.Payload, "json", d.Model, domain.FromDevice, received)
		matcher := Matcher{Plan: p, Device: d, SentAt: now}
		status, _ := matcher.Match(cap, event)
		if status != tc.want {
			t.Errorf("%+v: %q", tc, status)
		}
	}
	p, _ = Build(d, Request{Operation: "settings-get"}, now)
	for _, seconds := range []int64{now.Unix() - 600, now.Unix()} {
		cap := domain.Capture{Topic: d.Topics.Telemetry, Direction: domain.FromDevice, ReceivedAt: now.Add(time.Second), Payload: []byte(fmt.Sprintf(`{"type":"28","timestamp":%d,"setting":{"report_interval":900}}`, seconds))}
		event, _ := device.Decode(cap.Payload, "json", d.Model, domain.FromDevice, cap.ReceivedAt)
		matcher := Matcher{Plan: p, Device: d, SentAt: now}
		status, _ := matcher.Match(cap, event)
		if (status == "verified") != (seconds == now.Unix()) {
			t.Fatal(status, seconds)
		}
	}
}
func TestRealtimeDistinctFreshSamples(t *testing.T) {
	now := time.Now().UTC()
	d := fixture("CGDN1")
	p, _ := Build(d, Request{Operation: "realtime", Duration: time.Minute, Interval: 3 * time.Second, Verify: true}, now)
	m := Matcher{Plan: p, Device: d, SentAt: now}
	for i, stamp := range []int64{now.Unix() - 100, now.Unix(), now.Unix(), now.Unix() + 3} {
		raw := []byte(fmt.Sprintf(`{"type":"12","sensorData":[{"timestamp":{"value":%d},"co2":{"value":600}}]}`, stamp))
		c := domain.Capture{Topic: d.Topics.Telemetry, Direction: domain.FromDevice, Payload: raw, ReceivedAt: now.Add(4 * time.Second)}
		e, _ := device.Decode(raw, "json", d.Model, domain.FromDevice, c.ReceivedAt)
		s, _ := m.Match(c, e)
		if (s == "verified") != (i == 3) {
			t.Fatal(i, s, e)
		}
	}
}
func TestHEXSettingsAndClock(t *testing.T) {
	if _, err := Build(fixture("CGP22C"), Request{Operation: "settings-get"}, time.Now()); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unverified HEX query enabled", err)
	}
	now := time.Unix(1792000000, 0)
	d := fixture("CGP22C")
	p, _ := Build(d, Request{Operation: "sync-time"}, now)
	decoded, err := protocol.Decode(p.Payload, "hex")
	if err != nil || len(decoded.Entries) != 1 || decoded.Entries[0].Key != 0x15 || hex.EncodeToString(decoded.Entries[0].Value) != "00c0cf6a" {
		t.Fatal(decoded, err)
	}
	p = Plan{Operation: "settings-get", Protocol: "hex", Completion: "settings-report"}
	raw, _ := protocol.EncodeHEX(0x39, []protocol.TLV{{Key: 0x04, Value: []byte{15, 0}}})
	c := domain.Capture{Topic: d.Topics.Telemetry, Direction: domain.FromDevice, ReceivedAt: now.Add(time.Second), Payload: raw}
	e, _ := device.Decode(raw, "hex", d.Model, domain.FromDevice, c.ReceivedAt)
	m := Matcher{Plan: p, Device: d, SentAt: now}
	if status, _ := m.Match(c, e); status != "verified" {
		t.Fatal(e, status)
	}
}

package settings

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/protocol"
	"gopkg.in/yaml.v3"
)

func values(t *testing.T, s string) Values {
	t.Helper()
	var v Values
	if err := yaml.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestIndependentWireVectors(t *testing.T) {
	for _, tc := range []struct{ yaml, frame string }{
		{"reporting: {upload_interval: 5m}", "43473205000402000500cc00"},
		{"display: {clock_format: 24h}", "43473204003e010000ff00"},
		{"display: {temperature_unit: fahrenheit}", "434732040019010001db00"},
		{"clock: {timezone: '+02:00'}", "434732050088020014005f01"},
		{"clock: {sntp_enabled: true}", "4347320400440100010601"},
		{"raw: {hex: [{key: '0x06', value_hex: 'e803'}]}", "4347320500060200e803b401"},
	} {
		p, e := Build("hex", values(t, tc.yaml))
		if e != nil {
			t.Fatal(e)
		}
		b, e := p.Encode(0, time.Time{})
		if e != nil || hex.EncodeToString(b) != tc.frame {
			t.Fatalf("%s: %x %v", tc.yaml, b, e)
		}
	}
}
func TestScalesAndAlarm(t *testing.T) {
	p, e := Build("hex", values(t, `calibration:
  temperature: {offset_c: -1.2, offset_percent: 0}
threshold_alarms:
  - {sensor: co2, condition: above, threshold: 1400, unit: ppm, repeat: daily, start: '00:00', end: '23:00', sound_duration: 15s}
  - {sensor: pressure, condition: below, threshold: 101.23, unit: kpa, repeat: once, start: '00:00', end: '00:00'}
`))
	if e != nil {
		t.Fatal(e)
	}
	want := map[string]string{"0x46": "f4ff", "0x47": "0000", "0x39": "fe000000006405000078050f00", "0x0e": "0100000000000000008b27"}
	for _, f := range p.Fields {
		if hex.EncodeToString(f.HEX.Value) != want[f.Key] {
			t.Fatalf("%s %x", f.Key, f.HEX.Value)
		}
	}
	p, e = Build("json", values(t, `calibration:
  temperature: {offset_c: -1.01, offset_percent: 2}
  humidity: {offset_percent_rh: -1.1, offset_percent: 2.5}
display: {pages: [temperature, co2, temperature], clock_format: 12h}
`))
	if e != nil {
		t.Fatal(e)
	}
	wantJSON := map[string]string{"temperature_offset": "-101", "temperature_zoom": "2", "humidity_offset": "-11", "humidity_zoom": "25", "page_sequence": "\"temp,co2,temp\"", "is_12_hour_mode": "1"}
	for _, f := range p.Fields {
		if canonical(f.JSON) != wantJSON[f.Key] {
			t.Fatal(f.Key, f.JSON)
		}
	}
}
func TestValidationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		kind, input string
		ok          bool
	}{
		{"hex", "reporting: {upload_interval: 90s}", false},
		{"hex", "reporting: {upload_interval: 65535m}", true},
		{"hex", "reporting: {upload_interval: 65536m}", false},
		{"hex", "calibration: {temperature: {offset_c: -3276.8}}", true},
		{"hex", "calibration: {temperature: {offset_c: -3276.9}}", false},
		{"hex", "calibration: {temperature: {offset_c: 0.01}}", false},
		{"hex", "clock: {sntp_enabled: 1}", false},
		{"hex", "clock: {timezone: '+05:45'}", false},
		{"hex", "clock: {timezone: '-03:30'}", true},
		{"hex", "display: {clock_format: false}", false},
		{"hex", "power: {battery_off_after: 0s}", true},
		{"hex", "reporting: {high_frequency_interval: 18h}", true},
		{"hex", "reporting: {high_frequency_interval: 18h1s}", false},
		{"json", "reporting: {record_interval: 10s}", true},
		{"json", "reporting: {record_interval: 9s}", false},
		{"json", "reporting: {upload_interval: 10s}", false},
		{"json", "display: {rotate_interval: 120s}", true},
		{"json", "display: {rotate_interval: 61s}", false},
		{"json", "display: {night_mode: {start: '24:00'}}", false},
		{"hex", "display: {unknown: 1}", false},
		{"json", "clock: {sntp_enabled: false}", false},
		{"hex", "raw: {json: {foo: 1}}", false},
		{"hex", "raw: {hex: [{key: '0x06', value_hex: 'xx'}]}", false},
		{"hex", "reporting: {upload_interval: 5m}\nraw: {hex: [{key: '0x04', value_hex: '0500'}]}", false},
		{"json", "display: {clock_format: 24h}\nraw: {json: {is_12_hour_mode: 0}}", false},
	} {
		_, e := Build(tc.kind, values(t, tc.input))
		if (e == nil) != tc.ok {
			t.Errorf("%s %s: %v", tc.kind, tc.input, e)
		}
	}
}
func TestObserveAndCompare(t *testing.T) {
	b, _ := hex.DecodeString("43473205000402000500cc00")
	m, _ := protocol.Decode(b, "hex")
	fields := map[byte][][]byte{0x04: {m.Entries[0].Value}, 0x06: {{0xe8, 3}}, 0x38: {{0x33, 0}}, 0x01: {{1, 2}}}
	v, o := Observe("hex", fields, nil)
	p, e := Build("hex", v)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Fields) != 2 {
		t.Fatal(v)
	}
	for _, c := range p.Compare(o) {
		if c.Status != "matched" {
			t.Fatal(c)
		}
	}
	o["0x04"] = []string{"0500", "0600"}
	if p.Compare(o)[0].Status != "ambiguous" {
		t.Fatal(p.Compare(o))
	}
	_, o = Observe("json", nil, map[string]json.RawMessage{"collect_interval": []byte("10"), "secret": []byte(`"[redacted]"`)})
	if _, ok := o["secret"]; ok {
		t.Fatal("retained a secret")
	}
	p, _ = Build("json", Values{"raw": map[string]any{"json": map[string]any{"arbitrary": map[string]any{"a": true, "b": nil, "c": []any{1, "x"}}}}})
	wire, e := p.Encode(123, time.Unix(1792000000, 0))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(wire), `"b":null`) {
		t.Fatal(string(wire))
	}
	redacted, _ := json.Marshal(p.Redacted())
	if strings.Contains(string(redacted), `"arbitrary":`) {
		t.Fatal("raw leaked")
	}
}

func TestExportYAMLPreservesRawTypes(t *testing.T) {
	v := Values{"raw": map[string]any{"json": map[string]any{"number": json.Number("9223372036854775806"), "string": "00", "bool": false}}}
	b, e := yaml.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	var back Values
	if e = yaml.Unmarshal(b, &back); e != nil {
		t.Fatal(e)
	}
	p, e := Build("json", back)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range p.Fields {
		if f.Key == "number" && canonical(f.JSON) != "9223372036854775806" {
			t.Fatal(string(b), f)
		}
	}
}

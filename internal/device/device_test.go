package device

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/protocol"
	"github.com/danielskowronski/qiotlctl/internal/storage"
)

func TestHistoryCorpusMatchesPython(t *testing.T) {
	var corpus struct {
		Cases []struct {
			Name, Model, Protocol, Direction string
			Payload                          string          `json:"payload_hex"`
			Received                         string          `json:"received_at"`
			Expected                         []domain.Sample `json:"expected_samples"`
			Unsupported                      string          `json:"reference_unsupported"`
		}
	}
	b, err := os.ReadFile("../../testdata/history.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) < 20 {
		t.Fatal("incomplete corpus")
	}
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			raw, _ := hex.DecodeString(c.Payload)
			received, _ := storage.ParseTimestamp(c.Received)
			e, err := Decode(raw, c.Protocol, c.Model, domain.Direction(c.Direction), received)
			if err != nil {
				t.Fatal(err)
			}
			if c.Unsupported != "" {
				return
			}
			if len(e.Samples) != len(c.Expected) {
				t.Fatalf("samples got %d want %d", len(e.Samples), len(c.Expected))
			}
			for i, s := range e.Samples {
				want := c.Expected[i]
				if s.Timestamp != want.Timestamp {
					t.Fatal("timestamp mismatch")
				}
				got := map[string]domain.Reading{}
				for _, r := range s.Readings {
					got[r.Sensor] = r
				}
				for _, r := range want.Readings {
					v, ok := got[r.Sensor]
					if !ok || math.Abs(v.Value-r.Value) > 1e-8 || v.Unit != r.Unit {
						t.Errorf("reading %s got %+v want %+v", r.Sensor, v, r)
					}
				}
			}
		})
	}
}

func TestJSONStatusAndTime(t *testing.T) {
	e, err := Decode([]byte(`{"type":"12","sensorData":[{"timestamp":{"value":20},"temperature":{"value":-12.5,"status":2},"co2":{"value":0,"status":4},"battery":{"value":50,"status":1},"new_sensor":{"value":8}}]}`), "auto", "CGDN1", domain.FromDevice, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if e.Samples[0].TimeQuality != "before-2020" || len(e.Unknown) != 1 {
		t.Fatalf("%+v", e)
	}
	for _, r := range e.Samples[0].Readings {
		switch r.Sensor {
		case "battery":
			if r.Quality != "normal" || r.ChargingStatus == nil {
				t.Fatal(r)
			}
		case "co2":
			if r.Quality != "not-installed" {
				t.Fatal(r)
			}
		case "temperature":
			if r.Quality != "initializing" || r.Value != -12.5 {
				t.Fatal(r)
			}
		}
	}
	e, err = Decode([]byte(`{"type":"17","setting":{"report_interval":300}}`), "json", "CGDN1", domain.ToDevice, time.Time{})
	if err != nil || len(e.Samples) != 0 || e.Settings["upload_interval_seconds"] != float64(300) {
		t.Fatalf("%+v %v", e, err)
	}
}

func TestHEXTypedAndAmbiguity(t *testing.T) {
	// Vendor v2 example: 23.8 C, 16.6 % RH, 507 ppm.
	value, _ := hex.DecodeString("5253ca6704ee00a600fb01")
	raw, _ := protocol.EncodeHEX(0x42, []protocol.TLV{{Key: 0x85, Value: value}})
	e, err := Decode(raw, "hex", "", domain.FromDevice, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	s := e.Samples[0]
	if s.Timestamp != 1741312850 || s.Readings[0].Value != 23.8 || s.Readings[2].Value != 507 {
		t.Fatal(s)
	}
	value[5] = 0x85
	value[6] = 0xff
	raw, _ = protocol.EncodeHEX(0x43, []protocol.TLV{{Key: 0x85, Value: value}})
	e, _ = Decode(raw, "hex", "", domain.FromDevice, time.Time{})
	if e.Samples[0].Readings[0].Value != -12.3 {
		t.Fatal(e.Samples)
	}
	legacyValue, _ := hex.DecodeString("caa11b691e00794130270464ffffffffffff")
	raw, _ = protocol.EncodeHEX(0x31, []protocol.TLV{{Key: 3, Value: legacyValue}})
	e, err = Decode(raw, "hex", "", domain.FromDevice, time.Time{})
	if err != nil || len(e.Samples) != 1 || len(e.Unknown) == 0 || len(e.Warnings) == 0 {
		t.Fatalf("%+v %v", e, err)
	}
	for _, r := range e.Samples[0].Readings {
		if r.Sensor == "co2" || r.Sensor == "pressure" {
			t.Fatal("invented ambiguous sensor")
		}
	}
}

func FuzzModelDecode(f *testing.F) {
	f.Add([]byte(`{"type":"17"}`))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = Decode(b, "auto", "", domain.FromDevice, time.Time{}) })
}

func TestNestedSecretRedaction(t *testing.T) {
	e, err := Decode([]byte(`{"type":"5","vendor":{"credentials":{"password":"secret-value"}},"setting":{"nested":{"token":"secret-value"}}}`), "json", "CGDN1", domain.ToDevice, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(e)
	if bytes.Contains(b, []byte("secret-value")) {
		t.Fatal("nested secret leaked")
	}
}

func TestInclusiveYellowIndicator(t *testing.T) {
	// 0x3c carries the two inclusive yellow endpoints, not a green range.
	low, high := uint16(1000), uint16(1500)
	b, err := protocol.EncodeHEX(0x32, []protocol.TLV{{Key: 0x3c, Value: []byte{0xe8, 0x03, 0xdc, 0x05}}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := protocol.Decode(b, "hex")
	if err != nil || len(m.Entries) != 1 || m.Entries[0].Key != 0x3c || hex.EncodeToString(m.Entries[0].Value) != "e803dc05" {
		t.Fatalf("%x %v", b, err)
	}
	e, err := Decode(b, "hex", "CGP22C", domain.ToDevice, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bounds := e.Settings["co2_indicator"].(map[string]any)
	if bounds["yellow_min_ppm"] != low || bounds["yellow_max_ppm"] != high || bounds["inclusive"] != true {
		t.Fatal(bounds)
	}
}
func TestNonfiniteRSSIDoesNotBreakEvent(t *testing.T) {
	e, err := Decode([]byte(`{"type":"13","wifi_info":"fixture,NaN,6,00:11:22:33:44:55"}`), "json", "CGDN1", domain.FromDevice, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = json.Marshal(e); err != nil {
		t.Fatal("event cannot be serialized", err)
	}
}

func TestPressureAlarmUsesHundredthsOfKPa(t *testing.T) {
	b, err := hex.DecodeString("4347390e000e0b000100000000000000008b279d01")
	if err != nil {
		t.Fatal(err)
	}
	e, err := Decode(b, "hex", "CGP23W", domain.FromDevice, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Settings["alarm_0x0e"].(map[string]any)["threshold"]; got != 101.23 {
		t.Fatal(got)
	}
}

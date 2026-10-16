package output

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/storage"
)

func fixture(t *testing.T) (*Store, config.Device, time.Time) {
	t.Helper()
	c, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	d := config.Device{ID: "001122334455", MAC: "00:11:22:33:44:55", Alias: "bedroom", Model: "CGP22C", Protocol: "hex", HomeAssistant: config.HADevice{ID: "bedroom"}}
	c.Devices = map[string]config.Device{"bedroom": d}
	c.Outputs.VictoriaMetrics.Enabled = true
	c.Outputs.VictoriaMetrics.ImportEndpoint = "http://example.test/api/v1/import"
	c.Outputs.HomeAssistant.Enabled = true
	c.Storage.Path = filepath.Join(t.TempDir(), "state.sqlite")
	r, err := storage.OpenRecorder(c.Storage.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return NewStore(r, c), d, time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
}
func event(d config.Device, at time.Time, value float64) domain.Event {
	return domain.Event{DeviceID: d.ID, Alias: d.Alias, Model: d.Model, Protocol: d.Protocol, Direction: domain.FromDevice, ReceivedAt: at, Samples: []domain.Sample{{Timestamp: at.Unix(), TimeQuality: "device", Readings: []domain.Reading{{Sensor: "co2", Value: value, Unit: "ppm", Quality: "normal"}}}}}
}
func capture(t *testing.T, s *Store, e domain.Event) {
	t.Helper()
	ctx := context.Background()
	_, err := s.Recorder.Record(ctx, domain.Capture{ReceivedAt: e.ReceivedAt}, e, s.CaptureHook(ctx, e))
	if err != nil {
		t.Fatal(err)
	}
}
func scalar(t *testing.T, s *Store, query string) int64 {
	t.Helper()
	var n int64
	if err := s.Recorder.Database().QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestAtomicQueueDedupConflictAndRestart(t *testing.T) {
	s, d, now := fixture(t)
	e := event(d, now, 600)
	capture(t, s, e)
	capture(t, s, e)
	capture(t, s, event(d, now, 700))
	future := event(d, now, 900)
	future.Samples[0].Timestamp = now.Add(time.Hour).Unix()
	capture(t, s, future)
	if scalar(t, s, "SELECT count(*) FROM vm_samples") != 1 || scalar(t, s, "SELECT count(*) FROM output_rejections") != 2 {
		t.Fatal("dedup/quarantine failed")
	}
	ctx := context.Background()
	_, err := s.Recorder.Record(ctx, domain.Capture{ReceivedAt: now}, event(d, now.Add(time.Second), 800), s.CaptureHook(ctx, event(d, now.Add(time.Second), 800)), func(*sql.Tx) error { return errors.New("injected failure") })
	if err == nil || scalar(t, s, "SELECT count(*) FROM captures") != 4 || scalar(t, s, "SELECT count(*) FROM vm_samples") != 1 {
		t.Fatal("atomicity failed", err)
	}
	s.Recorder.Close()
	r, err := storage.OpenRecorder(s.Config.Storage.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	s.Recorder = r
	if scalar(t, s, "SELECT count(*) FROM vm_samples WHERE delivered=0") != 1 {
		t.Fatal("lost pending work on restart")
	}
	states, err := s.HAStates(ctx)
	if err != nil || len(states) != 1 || states[0].Reading.Value != 600 {
		t.Fatal(states, err)
	}
	r.MaxBytes = 1
	if _, err = r.Record(ctx, domain.Capture{}, e); err == nil {
		t.Fatal("capacity failure silently accepted")
	}
}
func TestPythonMetricContractAndOriginalUnits(t *testing.T) {
	s, d, now := fixture(t)
	e := event(d, now, 600)
	line, _, reason, err := PrepareVM(s.Config.Outputs.VictoriaMetrics, d, e, e.Samples[0], e.Samples[0].Readings[0])
	want := map[string]string{"__name__": "qingping_iot_co2", "device_mac": "001122334455", "device_name": "bedroom", "sensor": "co2", "unit": "ppm", "device_model": "CGP22C", "device_protocol": "hex"}
	if err != nil || reason != "" || !reflect.DeepEqual(line.Metric, want) || line.Timestamps[0] != now.Unix()*1000 {
		t.Fatal(line, reason, err)
	}
	raw := []byte(`{"type":"17","sensorData":[{"timestamp":{"value":1792144800},"temperature":{"value":77,"unit":"F"}}]}`)
	decoded, err := device.Decode(raw, "json", "CGDN1", domain.FromDevice, now)
	if err != nil {
		t.Fatal(err)
	}
	r := decoded.Samples[0].Readings[0]
	if r.Value != 25 || r.Unit != "°C" {
		t.Fatal(r)
	}
	v := s.Config.Outputs.VictoriaMetrics
	v.MetricsPrefix = ""
	d.Model = "CGDN1"
	line, _, _, err = PrepareVM(v, d, decoded, decoded.Samples[0], r)
	if err != nil || line.Values[0] != 77 || line.Metric["unit"] != "F" || line.Metric["__name__"] != "temperature" {
		t.Fatal(line, err)
	}
	v.MetricsPrefix = "custom"
	line, _, _, _ = PrepareVM(v, d, decoded, decoded.Samples[0], r)
	if line.Metric["__name__"] != "custom_temperature" {
		t.Fatal(line)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestVMFailureRecoveryAndUncertainCompletion(t *testing.T) {
	s, d, now := fixture(t)
	capture(t, s, event(d, now, 500))
	ctx := context.Background()
	stats := &Stats{}
	attempts := 0
	var bodies []string
	sender := &VMSender{Config: s.Config.Outputs.VictoriaMetrics, Client: &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		attempts++
		b, _ := io.ReadAll(req.Body)
		bodies = append(bodies, string(b))
		status := 204
		if attempts == 1 {
			status = 503
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("hidden upstream details")), Header: http.Header{}}, nil
	})}}
	worker := VMWorker{s, sender, stats}
	if _, err := worker.Flush(ctx); err == nil || scalar(t, s, "SELECT count(*) FROM vm_samples WHERE delivered=0") != 1 {
		t.Fatal("failure lost work")
	}
	// Fail the checkpoint after the remote endpoint has accepted the body.
	_, err := s.Recorder.Database().Exec(`CREATE TRIGGER fail_checkpoint BEFORE UPDATE OF delivered ON vm_samples BEGIN SELECT RAISE(ABORT,'injected crash boundary'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worker.Flush(ctx); err == nil {
		t.Fatal("checkpoint failure hidden")
	}
	if scalar(t, s, "SELECT count(*) FROM vm_samples WHERE delivered=0 AND payload<>''") != 1 {
		t.Fatal("uncommitted payload lost")
	}
	_, _ = s.Recorder.Database().Exec(`DROP TRIGGER fail_checkpoint`)
	if n, err := worker.Flush(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] || stats.VMSent.Load() != 1 {
		t.Fatal("retry changed payload")
	}
	if scalar(t, s, "SELECT count(*) FROM vm_samples WHERE delivered>0 AND payload=''") != 1 {
		t.Fatal("delivered payload not compacted")
	}

}

type mockPublisher struct {
	Calls []Publication
	Fail  bool
}

func (m *mockPublisher) Publish(_ context.Context, topic string, qos byte, retain bool, b []byte) error {
	if m.Fail {
		return errors.New("mock offline")
	}
	m.Calls = append(m.Calls, Publication{topic, retain, qos, append([]byte(nil), b...)})
	return nil
}
func TestHATemplateFreshnessLateHistoryAndBirth(t *testing.T) {
	s, d, now := fixture(t)
	ctx := context.Background()
	p, ok := Discovery(s.Config, d, "battery")
	if !ok {
		t.Fatal("no battery")
	}
	var payload map[string]any
	if err := json.Unmarshal(p.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["unique_id"] != "qingping_bedroom_battery" || payload["expire_after"] != float64(600) {
		t.Fatal(payload)
	}
	dev := payload["device"].(map[string]any)
	if dev["name"] != "Qingping Bedroom" || dev["model"] != "CGP22C - CO2 & Temp & RH Monitor" || dev["identifiers"].([]any)[0] != "qingping_001122334455" {
		t.Fatal(dev)
	}
	capture(t, s, event(d, now, 700))
	capture(t, s, event(d, now.Add(-time.Minute), 500))
	mock := &mockPublisher{}
	worker := HAWorker{Store: s, Stats: &Stats{}, Publisher: mock}
	if err := worker.Sync(ctx, now, true); err != nil {
		t.Fatal(err)
	}
	stateTopic := SensorTopic(s.Config.Outputs.HomeAssistant, d, "co2") + "/state"
	countState := func() int {
		n := 0
		for _, p := range mock.Calls {
			if p.Topic == stateTopic {
				n++
				if p.Retain || !strings.Contains(string(p.Payload), `"value":700`) {
					t.Fatal(p)
				}
			}
		}
		return n
	}
	if countState() != 1 {
		t.Fatal(mock.Calls)
	}
	if err := worker.Sync(ctx, now.Add(time.Second), true); err != nil {
		t.Fatal(err)
	}
	if countState() != 2 {
		t.Fatal("birth did not restore state")
	}
	mock.Calls = nil
	// RSSI heartbeat has its own timestamp; it cannot freshen CO2.
	heartbeat := event(d, now.Add(11*time.Minute), -55)
	heartbeat.Samples[0].Readings[0] = domain.Reading{Sensor: "signal_strength", Value: -55, Unit: "dBm", Quality: "normal"}
	capture(t, s, heartbeat)
	if err := worker.Sync(ctx, now.Add(11*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	if countState() != 0 {
		t.Fatal("stale history published")
	}
	foundOffline := false
	for _, p := range mock.Calls {
		if p.Topic == strings.TrimSuffix(stateTopic, "/state")+"/availability" && string(p.Payload) == "offline" {
			foundOffline = true
		}
	}
	if !foundOffline {
		t.Fatal("stale sensor still available")
	}
	mock.Fail = true
	capture(t, s, event(d, now.Add(12*time.Minute), 800))
	if err := worker.Sync(ctx, now.Add(12*time.Minute), false); err == nil {
		t.Fatal("failure hidden")
	}
	mock.Fail = false
	if err := worker.Sync(ctx, now.Add(12*time.Minute), true); err != nil {
		t.Fatal(err)
	}
}

func TestHAExpiryBoundaryAndInvalidQuality(t *testing.T) {
	s, d, now := fixture(t)
	ctx := context.Background()
	capture(t, s, event(d, now, 500))
	mock := &mockPublisher{}
	w := HAWorker{Store: s, Stats: &Stats{}, Publisher: mock}
	if err := w.Sync(ctx, now.Add(600*time.Second), true); err != nil {
		t.Fatal(err)
	}
	topic := SensorTopic(s.Config.Outputs.HomeAssistant, d, "co2")
	for _, p := range mock.Calls {
		if p.Topic == topic+"/state" || p.Topic == topic+"/availability" && string(p.Payload) != "offline" {
			t.Fatal("value valid at expiry boundary", p)
		}
	}
	invalid := event(d, now.Add(601*time.Second), 65535)
	invalid.Samples[0].Readings[0].Quality = "sensor-error"
	capture(t, s, invalid)
	mock.Calls = nil
	if err := w.Sync(ctx, now.Add(601*time.Second), true); err != nil {
		t.Fatal(err)
	}
	unknown := false
	for _, p := range mock.Calls {
		if p.Topic == topic+"/state" {
			unknown = strings.Contains(string(p.Payload), `"value":null`)
		}
	}
	if !unknown {
		t.Fatal("invalid quality wasn't unknown")
	}
}

func TestHAModelChangeDoesNotRecreateUnsupportedSensors(t *testing.T) {
	s, d, now := fixture(t)
	// First discover a PM sensor using a generic device, then correct its model.
	generic := d
	generic.Model = ""
	s.Config.Devices[d.Alias] = generic
	e := event(generic, now, 10)
	e.Samples[0].Readings[0] = domain.Reading{Sensor: "pm25", Value: 10, Unit: "µg/m³", Quality: "normal"}
	capture(t, s, e)
	states, err := s.HAStates(context.Background())
	if err != nil || len(states) != 1 {
		t.Fatal(states, err)
	}
	if len(ExpectedDiscovery(s.Config, states)) != 1 {
		t.Fatal("generic observed sensor missing")
	}
	s.Config.Devices[d.Alias] = d
	for _, p := range ExpectedDiscovery(s.Config, states) {
		if strings.Contains(p.Topic, "/pm25/") {
			t.Fatal("doctor expects old unsupported sensor", p.Topic)
		}
	}
	publisher := &mockPublisher{}
	worker := &HAWorker{Store: s, Stats: &Stats{}, Publisher: publisher}
	if err := worker.Sync(context.Background(), now, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range publisher.Calls {
		if strings.Contains(p.Topic, "/pm25/") {
			t.Fatal("worker recreated unsupported sensor", p.Topic)
		}
	}
}
func TestHAStateTopicEscapesMQTTWildcards(t *testing.T) {
	s, d, _ := fixture(t)
	d.ID = "room+1/#"
	topic := SensorTopic(s.Config.Outputs.HomeAssistant, d, "co2")
	if strings.ContainsAny(topic, "+#") || !strings.Contains(topic, "room%2B1%2F%23") {
		t.Fatal(topic)
	}
}

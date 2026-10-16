package backfill

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/output"
	"github.com/danielskowronski/qiotlctl/internal/storage"
)

func setup(t *testing.T) (config.Config, *output.Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "history.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sensor_readings(id INTEGER PRIMARY KEY,timestamp TEXT,mac TEXT,name TEXT,sensor TEXT,value REAL,unit TEXT,status TEXT);
 INSERT INTO sensor_readings VALUES
 (1,'2026-10-15 10:00:00+00:00','AABBCCDDEEFF','old-room','CO2',500,'ppm','ok'),
 (2,'2026-10-15 10:01:00+00:00','AABBCCDDEEFF','old-room','CO2',600,'ppm','ok'),
 (3,'2026-10-15 10:01:00+00:00','AABBCCDDEEFF','old-room','CO2',600,'ppm','ok'),
 (4,'1970-01-01 00:00:01+00:00','AABBCCDDEEFF','old-room','CO2',700,'ppm','ok');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	c, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Devices = map[string]config.Device{"new-room": {ID: "AABBCCDDEEFF", MAC: "AABBCCDDEEFF", Alias: "new-room", Model: "CGP22C", Protocol: "hex"}}
	c.Outputs.VictoriaMetrics.ImportEndpoint = "http://example.test/api/v1/import"
	c.Storage.Path = filepath.Join(dir, "state.sqlite")
	r, err := storage.OpenRecorder(c.Storage.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	s := output.NewStore(r, c)
	s.Backfill = true
	return c, s, path
}
func TestBackfillResumeReadOnlyAndContractGuard(t *testing.T) {
	c, s, path := setup(t)
	ctx := context.Background()
	before, _ := os.ReadFile(path)
	o := Options{Path: path, Job: "migration", Source: "auto", Limit: 2}
	p, err := Run(ctx, c, s, o, nil, func(Progress) error { return errors.New("simulated output interruption") })
	if err == nil || p.Cursor != 2 {
		t.Fatal(p, err)
	}
	// A persisted checkpoint advances only with enqueued rows, even if delivery later fails.
	o.Limit = 0
	p, err = Run(ctx, c, s, o, nil, nil)
	if err != nil || !p.Complete || p.Processed != 2 {
		t.Fatal(p, err)
	}
	var n int
	if err = s.Recorder.Database().QueryRow(`SELECT count(*) FROM vm_samples`).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	var payload string
	_ = s.Recorder.Database().QueryRow(`SELECT payload FROM vm_samples ORDER BY id LIMIT 1`).Scan(&payload)
	if payload == "" {
		t.Fatal("no frozen payload")
	}
	var rejected int
	_ = s.Recorder.Database().QueryRow(`SELECT count(*) FROM output_rejections`).Scan(&rejected)
	if rejected != 1 {
		t.Fatal(rejected)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("source modified")
	}
	c.Outputs.VictoriaMetrics.MetricsPrefix = "other_"
	if _, err = Run(ctx, c, s, o, nil, nil); err == nil {
		t.Fatal("changed mapping resumed")
	}
}
func TestPreviewUsesMeasurementTimeAndNoWrites(t *testing.T) {
	c, s, path := setup(t)
	ctx := context.Background()
	since := time.Date(2026, 10, 15, 10, 1, 0, 0, time.UTC)
	var lines []output.VMLine
	p, err := Run(ctx, c, nil, Options{Path: path, DryRun: true, Since: since, Until: since.Add(time.Second)}, func(v output.VMLine) error { lines = append(lines, v); return nil }, nil)
	if err != nil || p.Samples != 2 || len(lines) != 2 {
		t.Fatal(p, lines, err)
	}
	if lines[0].Metric["device_name"] != "old-room" || lines[0].Metric["device_model"] != "CGP22C" || lines[0].Timestamps[0] != since.UnixMilli() {
		t.Fatal(lines[0])
	}
	var n int
	_ = s.Recorder.Database().QueryRow(`SELECT count(*) FROM backfill_jobs`).Scan(&n)
	if n != 0 {
		t.Fatal("preview wrote checkpoint")
	}
	if _, err = Run(ctx, c, nil, Options{Path: path, DryRun: true, Since: since, Until: since}, nil, nil); err == nil {
		t.Fatal("empty interval accepted")
	}
}
func TestChangedSnapshotRefusesResume(t *testing.T) {
	c, s, path := setup(t)
	o := Options{Path: path, Job: "job", Limit: 1}
	if _, err := Run(context.Background(), c, s, o, nil, nil); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", path)
	_, err := db.Exec(`UPDATE sensor_readings SET value=42 WHERE id=2`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Run(context.Background(), c, s, o, nil, nil); err == nil {
		t.Fatal("changed source resumed")
	}
}

func TestGoNormalizedJournalBackfill(t *testing.T) {
	c, s, _ := setup(t)
	path := filepath.Join(t.TempDir(), "go-snapshot.sqlite")
	d := c.Devices["new-room"]
	d.Topics.Telemetry = "fixture/up"
	c.Devices["new-room"] = d
	r, err := storage.OpenRecorder(path, false)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	e := domain.Event{DeviceID: d.ID, Model: d.Model, Protocol: "hex", Direction: domain.FromDevice, ReceivedAt: stamp, Samples: []domain.Sample{{Timestamp: stamp.Unix(), Readings: []domain.Reading{{Sensor: "co2", Value: 777, Unit: "ppm", Quality: "normal"}}}}}
	_, err = r.Record(context.Background(), domain.Capture{ReceivedAt: stamp, Topic: d.Topics.Telemetry, Direction: domain.FromDevice}, e)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	var got []output.VMLine
	p, err := Run(context.Background(), c, s, Options{Path: path, DryRun: true, Source: "auto"}, func(v output.VMLine) error { got = append(got, v); return nil }, nil)
	if err != nil || p.Samples != 1 || len(got) != 1 || got[0].Values[0] != 777 {
		t.Fatal(p, got, err)
	}
}

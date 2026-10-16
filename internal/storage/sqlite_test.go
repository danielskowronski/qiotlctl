package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/domain"
)

func TestCaptureReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.sqlite")
	r, err := OpenRecorder(path, true)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 15, 12, 0, 0, 123456789, time.UTC)
	qos := byte(1)
	retained := false
	c := domain.Capture{Topic: "fixture/up", Payload: []byte(`{"type":"10"}`), Direction: domain.FromDevice, ReceivedAt: stamp, QoS: &qos, Retained: &retained}
	e := domain.Event{SchemaVersion: 1, DeviceID: "fixture", ReceivedAt: stamp, Samples: []domain.Sample{{Timestamp: stamp.Unix(), TimeQuality: "device", Readings: []domain.Reading{{Sensor: "co2", Value: 500, Unit: "ppm", Quality: "normal"}}}}}
	for i := 0; i < 3; i++ {
		if _, err = r.Record(context.Background(), c, e); err != nil {
			t.Fatal(err)
		}
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	seen := 0
	err = Replay(context.Background(), path, ReplayOptions{AfterID: 1, Limit: 1, Topics: []string{"fixture/up"}}, func(got domain.Capture) error {
		seen++
		if got.ID != 2 || got.Direction != domain.FromDevice || !got.ReceivedAt.Equal(stamp) || got.QoS == nil || *got.QoS != 1 || string(got.Payload) != string(c.Payload) {
			t.Fatalf("%+v", got)
		}
		return nil
	})
	if err != nil || seen != 1 {
		t.Fatalf("%d %v", seen, err)
	}
	reopened, err := OpenRecorder(path, true)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
}

func TestLegacyIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE raw_payloads(id INTEGER PRIMARY KEY,timestamp TEXT,topic TEXT,payload BLOB); INSERT INTO raw_payloads VALUES(1,'2025-12-20 15:10:30.978626+00:00','fixture/up',x'4347');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	err = Replay(context.Background(), path, ReplayOptions{}, func(c domain.Capture) error {
		count++
		if c.Direction != "" || c.QoS != nil || c.ReceivedAt.Nanosecond() != 978626000 {
			t.Fatal(c)
		}
		return nil
	})
	if err != nil || count != 1 {
		t.Fatalf("%d %v", count, err)
	}
	if r, err := OpenRecorder(path, true); err == nil {
		r.Close()
		t.Fatal("allowed modifying legacy DB")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("legacy DB modified")
	}
	missing := filepath.Join(t.TempDir(), "absent.sqlite")
	if err = Replay(context.Background(), missing, ReplayOptions{}, func(domain.Capture) error { return nil }); err == nil {
		t.Fatal("missing DB accepted")
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("read-only replay created DB")
	}
}

func TestCredentialPayloadExcluded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.sqlite")
	r, err := OpenRecorder(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, raw := range []string{
		`{"type":"5","setting":{"mqtt_password":"sensitive"}}`,
		`{"type":"5","setting":{"host":"mqtt://user:sensitive@example.test"}}`,
	} {
		c := domain.Capture{Topic: "fixture/down", Payload: []byte(raw), Direction: domain.ToDevice, ReceivedAt: time.Now().UTC()}
		id, err := r.Record(context.Background(), c, domain.Event{SchemaVersion: 1})
		if err != nil {
			t.Fatal(err)
		}
		var payload []byte
		if err = r.db.QueryRow("SELECT payload FROM captures WHERE id=?", id).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if payload != nil {
			t.Fatal("credential-bearing packet was recorded")
		}
	}
}

func TestNormalizedOnlyReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "normalized.sqlite")
	r, err := OpenRecorder(path, false)
	if err != nil {
		t.Fatal(err)
	}
	e := domain.Event{SchemaVersion: 1, Alias: "fixture", Protocol: "json", MessageType: "13", Direction: domain.FromDevice, ReceivedAt: time.Now().UTC()}
	if _, err = r.Record(context.Background(), domain.Capture{ReceivedAt: e.ReceivedAt, Topic: "fixture/up", Direction: domain.FromDevice, Payload: []byte(`{"type":"13"}`)}, e); err != nil {
		t.Fatal(err)
	}
	r.Close()
	err = Replay(context.Background(), path, ReplayOptions{}, func(c domain.Capture) error {
		if c.Payload != nil || c.StoredEvent == nil || c.StoredEvent.MessageType != "13" {
			t.Fatal(c)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeV1PreservesCapturesAndCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.sqlite")
	r, err := OpenRecorder(path, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Record(context.Background(), domain.Capture{ReceivedAt: time.Now(), Topic: "fixture/up"}, domain.Event{SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.RecordCommand(context.Background(), domain.CommandResult{OperationID: "before-upgrade"}); err != nil {
		t.Fatal(err)
	}
	_, err = r.db.Exec(`DROP TABLE vm_samples; DROP TABLE ha_state; DROP TABLE ha_discovery; DROP TABLE output_rejections; DROP TABLE backfill_jobs; DROP TABLE output_status; PRAGMA user_version=1;`)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	r, err = OpenRecorder(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, table := range []string{"captures", "command_events"} {
		var n int
		if err = r.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 1 {
			t.Fatal(table, n, err)
		}
	}
	var version int
	if err = r.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal(version, err)
	}
}

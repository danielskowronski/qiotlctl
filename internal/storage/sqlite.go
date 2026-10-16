// Package storage records observations and streams new or legacy SQLite captures.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/protocol"
	_ "modernc.org/sqlite"
)

type Recorder struct {
	db       *sql.DB
	raw      bool
	path     string
	MaxBytes int64
}

func OpenRecorder(path string, raw bool) (*Recorder, error) {
	if path == "" {
		return nil, errors.New("empty recorder path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		_ = f.Close()
	} else if !os.IsExist(err) {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*Recorder, error) { db.Close(); return nil, err }
	var tables int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return fail(err)
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if tables > 0 && version != 1 && version != 2 {
		return fail(errors.New("refusing to modify an unrecognized or legacy database; choose a new capture path"))
	}
	if tables > 0 {
		var captures int
		if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='captures'").Scan(&captures); err != nil || captures != 1 {
			return fail(errors.New("database is not a qiotlctl recorder"))
		}
	}
	_, err = db.Exec(`PRAGMA journal_mode=WAL;
PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS captures (
 id INTEGER PRIMARY KEY, received_at TEXT NOT NULL, topic TEXT NOT NULL,
 direction TEXT NOT NULL, payload BLOB, qos INTEGER, retained INTEGER, duplicate INTEGER,
 event_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS readings (
 capture_id INTEGER NOT NULL, sample_index INTEGER NOT NULL, reading_index INTEGER NOT NULL,
 device_id TEXT NOT NULL, sensor TEXT NOT NULL, timestamp INTEGER NOT NULL,
 value REAL NOT NULL, unit TEXT NOT NULL, quality TEXT NOT NULL, time_quality TEXT NOT NULL,
 PRIMARY KEY(capture_id, sample_index, reading_index)
);
CREATE INDEX IF NOT EXISTS readings_device_time ON readings(device_id, timestamp);
CREATE TABLE IF NOT EXISTS command_events (
 id INTEGER PRIMARY KEY, operation_id TEXT NOT NULL, recorded_at TEXT NOT NULL,
 result_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS command_events_operation ON command_events(operation_id);
`)
	if err != nil {
		return fail(err)
	}
	if err = migrateOutputs(db); err != nil {
		return fail(err)
	}
	return &Recorder{db: db, raw: raw, path: abs}, nil
}
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	return r.db.Close()
}
func nullable[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
func (r *Recorder) Record(ctx context.Context, c domain.Capture, e domain.Event, hooks ...func(*sql.Tx) error) (int64, error) {
	if err := r.CheckCapacity(); err != nil {
		return 0, err
	}
	var payload any
	if r.raw && !protocol.ContainsCredentials(c.Payload) {
		payload = c.Payload
	} else {
		e.Warnings = append(e.Warnings, "raw payload omitted; replay uses stored normalized event")
	}
	b, err := json.Marshal(e)
	if err != nil {
		return 0, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "INSERT INTO captures(received_at,topic,direction,payload,qos,retained,duplicate,event_json) VALUES(?,?,?,?,?,?,?,?)", c.ReceivedAt.UTC().Format(time.RFC3339Nano), c.Topic, string(c.Direction), payload, nullable(c.QoS), nullable(c.Retained), nullable(c.Duplicate), string(b))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO readings VALUES(?,?,?,?,?,?,?,?,?,?)")
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for i, s := range e.Samples {
		for j, v := range s.Readings {
			if _, err = stmt.ExecContext(ctx, id, i, j, e.DeviceID, v.Sensor, s.Timestamp, v.Value, v.Unit, v.Quality, s.TimeQuality); err != nil {
				return 0, err
			}
		}
	}
	for _, hook := range hooks {
		if err = hook(tx); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

type ReplayOptions struct {
	AfterID      int64
	UntilID      int64
	Limit        int
	Topics       []string
	Since, Until time.Time
}

func Replay(ctx context.Context, path string, opts ReplayOptions, visit func(domain.Capture) error) error {
	if opts.AfterID < 0 || opts.Limit < 0 {
		return errors.New("replay bounds must be nonnegative")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var own int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='captures'").Scan(&own); err != nil {
		return err
	}
	query := "SELECT id,timestamp,topic,payload,NULL,NULL,NULL,'','' FROM raw_payloads WHERE id>?"
	if own == 1 {
		query = "SELECT id,received_at,topic,payload,qos,retained,duplicate,direction,event_json FROM captures WHERE id>?"
	}
	args := []any{opts.AfterID}
	if opts.UntilID > 0 {
		query += " AND id<=?"
		args = append(args, opts.UntilID)
	}
	if len(opts.Topics) > 0 {
		query += " AND topic IN (" + strings.TrimSuffix(strings.Repeat("?,", len(opts.Topics)), ",") + ")"
		for _, topic := range opts.Topics {
			args = append(args, topic)
		}
	}
	query += " ORDER BY id"
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("read capture database: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var c domain.Capture
		var stamp, dir, storedJSON string
		var qos, retained, duplicate sql.NullInt64
		if err = rows.Scan(&c.ID, &stamp, &c.Topic, &c.Payload, &qos, &retained, &duplicate, &dir, &storedJSON); err != nil {
			return err
		}
		c.ReceivedAt, err = ParseTimestamp(stamp)
		if err != nil {
			return fmt.Errorf("record %d has invalid receive timestamp", c.ID)
		}
		if !opts.Since.IsZero() && c.ReceivedAt.Before(opts.Since) || !opts.Until.IsZero() && !c.ReceivedAt.Before(opts.Until) {
			continue
		}
		if c.Payload == nil && storedJSON != "" {
			var event domain.Event
			if json.Unmarshal([]byte(storedJSON), &event) != nil {
				return fmt.Errorf("record %d has invalid stored event", c.ID)
			}
			c.StoredEvent = &event
		}
		c.Direction = domain.Direction(dir)
		if qos.Valid {
			v := byte(qos.Int64)
			c.QoS = &v
		}
		if retained.Valid {
			v := retained.Int64 != 0
			c.Retained = &v
		}
		if duplicate.Valid {
			v := duplicate.Int64 != 0
			c.Duplicate = &v
		}
		if err = visit(c); err != nil {
			return err
		}
		count++
		if opts.Limit > 0 && count >= opts.Limit {
			break
		}
	}
	return rows.Err()
}

func ParseTimestamp(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("invalid timestamp")
}

// RecordCommand appends a state transition; it never stores an unredacted raw command.
func (r *Recorder) RecordCommand(ctx context.Context, result domain.CommandResult) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, "INSERT INTO command_events(operation_id,recorded_at,result_json) VALUES(?,?,?)", result.OperationID, time.Now().UTC().Format(time.RFC3339Nano), string(b))
	return err
}

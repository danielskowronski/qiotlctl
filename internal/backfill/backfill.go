// Package backfill streams a read-only snapshot into the durable VM queue.
// It never connects to MQTT or updates Home Assistant state.
package backfill

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/observer"
	"github.com/danielskowronski/qiotlctl/internal/output"
	"github.com/danielskowronski/qiotlctl/internal/storage"
)

type Options struct {
	Path, Source, Job, MessageType string
	Since, Until                   time.Time
	AfterID                        int64
	Limit                          int
	DryRun                         bool
}
type Progress struct {
	Cursor    int64 `json:"cursor"`
	UpperID   int64 `json:"upper_id"`
	Processed int64 `json:"processed"`
	Samples   int64 `json:"samples"`
	Skipped   int64 `json:"skipped"`
	Rejected  int64 `json:"rejected"`
	Complete  bool  `json:"complete"`
}
type Row struct {
	ID     int64
	Device config.Device
	Event  domain.Event
}

func fingerprint(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return "", err
	}
	var parts []any
	parts = append(parts, real)
	for _, suffix := range []string{"", "-wal"} {
		s, e := os.Stat(real + suffix)
		if os.IsNotExist(e) {
			parts = append(parts, nil)
			continue
		}
		if e != nil {
			return "", e
		}
		if suffix == "-wal" && s.Size() == 0 {
			parts = append(parts, nil)
			continue
		}
		parts = append(parts, []int64{s.Size(), s.ModTime().UnixNano()})
	}
	return output.Hash(parts), nil
}
func Run(ctx context.Context, c config.Config, store *output.Store, o Options, preview func(output.VMLine) error, progress func(Progress) error) (Progress, error) {
	p := Progress{Cursor: o.AfterID}
	if o.Path == "" || o.AfterID < 0 || o.Limit < 0 {
		return p, errors.New("backfill needs --sqlite and nonnegative bounds")
	}
	if !o.Until.IsZero() && !o.Since.IsZero() && !o.Since.Before(o.Until) {
		return p, errors.New("--since must precede --until")
	}
	if !o.DryRun && (store == nil || o.Job == "") {
		return p, errors.New("backfill requires a durable state database and job name")
	}
	fp, err := fingerprint(o.Path)
	if err != nil {
		return p, err
	}
	db, err := storage.OpenReadOnly(o.Path)
	if err != nil {
		return p, err
	}
	defer db.Close()
	var legacy, own int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='sensor_readings'`).Scan(&legacy); err != nil {
		return p, err
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='captures'`).Scan(&own); err != nil {
		return p, err
	}
	source := o.Source
	if source == "auto" || source == "" {
		if legacy > 0 {
			source = "readings"
		} else {
			source = "raw"
		}
	}
	if source != "readings" && source != "raw" {
		return p, errors.New("source must be auto, readings or raw")
	}
	if source == "readings" && (legacy == 0 || o.MessageType != "") {
		return p, errors.New("readings source requires Python sensor_readings and cannot filter message type")
	}
	table := "sensor_readings"
	if source == "raw" {
		table = "raw_payloads"
		if own > 0 {
			table = "captures"
		}
	}
	if err = db.QueryRowContext(ctx, `SELECT coalesce(max(id),0) FROM `+table).Scan(&p.UpperID); err != nil {
		return p, err
	}
	// Credentials and batching controls do not change sample identity or selection.
	mapping := map[string]any{}
	for alias, d := range c.Devices {
		mapping[alias] = []any{d.ID, d.MAC, d.Model, d.Protocol, d.Topics, d.Active()}
	}
	contract := output.Hash([]any{fp, source, o.Since, o.Until, o.AfterID, o.MessageType, mapping, c.Outputs.VictoriaMetrics.ImportEndpoint, c.Outputs.VictoriaMetrics.MetricsPrefix, c.Outputs.VictoriaMetrics.IncludeInvalidTime, c.Outputs.VictoriaMetrics.NormalOnly})
	if !o.DryRun {
		var saved string
		var cursor, upper int64
		err = store.Recorder.Database().QueryRowContext(ctx, `SELECT fingerprint,cursor,upper_id FROM backfill_jobs WHERE name=?`, o.Job).Scan(&saved, &cursor, &upper)
		if err == nil {
			if saved != contract || upper != p.UpperID {
				return p, errors.New("job source or export configuration changed; use the original snapshot/settings or a new --job")
			}
			p.Cursor = cursor
		} else if errors.Is(err, sql.ErrNoRows) {
			_, err = store.Recorder.Database().ExecContext(ctx, `INSERT INTO backfill_jobs(name,fingerprint,upper_id,cursor) VALUES(?,?,?,?)`, o.Job, contract, p.UpperID, p.Cursor)
		}
		if err != nil {
			return p, err
		}
	}
	routes := observer.Routes(c)
	devices := map[string]config.Device{}
	for _, d := range c.Devices {
		devices[strings.ToUpper(strings.ReplaceAll(d.MAC, ":", ""))] = d
	}
	for p.Cursor < p.UpperID && (o.Limit == 0 || p.Processed < int64(o.Limit)) {
		n := 500
		if o.Limit > 0 {
			n = min(n, o.Limit-int(p.Processed))
		}
		page, err := readPage(ctx, db, c, routes, devices, table, p.Cursor, p.UpperID, n)
		if err != nil {
			return p, err
		}
		if len(page) == 0 {
			p.Cursor = p.UpperID
			break
		}
		var tx *sql.Tx
		if !o.DryRun {
			if err = store.Recorder.CheckCapacity(); err != nil {
				return p, err
			}
			tx, err = store.Recorder.Database().BeginTx(ctx, nil)
			if err != nil {
				return p, err
			}
		}
		apply := func() error {
			for _, row := range page {
				p.Cursor = row.ID
				p.Processed++
				e, d := row.Event, row.Device
				if !d.Active() || d.ID == "" || e.Direction != domain.FromDevice || o.MessageType != "" && e.MessageType != o.MessageType {
					p.Skipped++
					continue
				}
				if e.Error != "" {
					return fmt.Errorf("source row %d: %s", row.ID, e.Error)
				}
				for _, sm := range e.Samples {
					stamp := time.Unix(sm.Timestamp, 0)
					if !o.Since.IsZero() && stamp.Before(o.Since) || !o.Until.IsZero() && !stamp.Before(o.Until) {
						continue
					}
					for _, r := range sm.Readings {
						if !output.Allowed(e.Model, r.Sensor) {
							continue
						}
						line, _, reason, err := output.PrepareVM(c.Outputs.VictoriaMetrics, d, e, sm, r)
						if err != nil {
							return err
						}
						if reason != "" {
							p.Rejected++
						} else {
							p.Samples++
						}
						if o.DryRun {
							if reason == "" && preview != nil {
								if err = preview(line); err != nil {
									return err
								}
							}
						} else {
							if err = store.EnqueueVM(ctx, tx, d, e, sm, r); err != nil {
								return err
							}
						}
					}
				}
			}
			if tx != nil {
				_, err := tx.ExecContext(ctx, `UPDATE backfill_jobs SET cursor=max(cursor,?),processed=processed+? WHERE name=?`, p.Cursor, len(page), o.Job)
				return err
			}
			return nil
		}
		err = apply()
		if tx != nil {
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		if err != nil {
			return p, err
		}
		if progress != nil {
			if err = progress(p); err != nil {
				return p, err
			}
		}
	}
	p.Complete = p.Cursor >= p.UpperID
	// A changing source is not a resumable snapshot. Already queued data remains recoverable.
	after, err := fingerprint(o.Path)
	if err != nil {
		return p, err
	}
	if after != fp {
		return p, errors.New("source changed during backfill; use a consistent snapshot")
	}
	return p, nil
}
func readPage(ctx context.Context, db *sql.DB, c config.Config, routes map[string]observer.Route, devices map[string]config.Device, table string, after, upper int64, n int) ([]Row, error) {
	query := `SELECT id,timestamp,mac,name,sensor,value,unit,status FROM sensor_readings WHERE id>? AND id<=? ORDER BY id LIMIT ?`
	if table == "raw_payloads" {
		query = `SELECT id,timestamp,topic,payload,'' FROM raw_payloads WHERE id>? AND id<=? ORDER BY id LIMIT ?`
	}
	if table == "captures" {
		query = `SELECT id,received_at,topic,payload,event_json FROM captures WHERE id>? AND id<=? ORDER BY id LIMIT ?`
	}
	rows, err := db.QueryContext(ctx, query, after, upper, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var page []Row
	for rows.Next() {
		var row Row
		var stamp string
		if table == "sensor_readings" {
			var mac, alias, sensor, unit, status string
			var value float64
			if err = rows.Scan(&row.ID, &stamp, &mac, &alias, &sensor, &value, &unit, &status); err != nil {
				return nil, err
			}
			d, ok := devices[strings.ToUpper(strings.ReplaceAll(mac, ":", ""))]
			if !ok {
				page = append(page, row)
				continue
			}
			// Keep historical aliases; the configured model/protocol supply absent legacy columns.
			d.Alias = alias
			row.Device = d
			t, err := storage.ParseTimestamp(stamp)
			if err != nil {
				return nil, err
			}
			row.Event = domain.Event{DeviceID: d.ID, Alias: alias, Model: d.Model, Protocol: d.Protocol, Direction: domain.FromDevice, ReceivedAt: time.Now().UTC(), Samples: []domain.Sample{{Timestamp: t.Unix(), Readings: []domain.Reading{{Sensor: strings.ToLower(sensor), Value: value, Unit: unit, Quality: status}}}}}
		} else {
			var topic, stored string
			var payload []byte
			if err = rows.Scan(&row.ID, &stamp, &topic, &payload, &stored); err != nil {
				return nil, err
			}
			route, ok := routes[topic]
			if !ok {
				page = append(page, row)
				continue
			}
			row.Device = route.Device
			t, err := storage.ParseTimestamp(stamp)
			if err != nil {
				return nil, err
			}
			capture := domain.Capture{ID: row.ID, ReceivedAt: t, Topic: topic, Payload: payload}
			if payload == nil && stored != "" {
				var e domain.Event
				if err = json.Unmarshal([]byte(stored), &e); err != nil {
					return nil, err
				}
				capture.StoredEvent = &e
			}
			row.Event = observer.DecodeCapture(routes, capture)
		}
		page = append(page, row)
	}
	return page, rows.Err()
}

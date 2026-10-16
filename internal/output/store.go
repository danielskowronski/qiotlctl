// Package output provides durable, independently delivered VM and HA outputs.
package output

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/device"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/domain"
	"github.com/danielskowronski/qiotlctl/internal/storage"
)

type Stats struct {
	Diagnostics                                        *diagnostic.Registry
	VMSent, VMErrors, HAPublished, HAErrors            atomic.Uint64
	VMPending, VMOldestSeconds, Rejected, StorageBytes atomic.Int64
	VMHealthy, HAHealthy                               atomic.Bool
}
type Store struct {
	Recorder *storage.Recorder
	Config   config.Config
	Target   string
	Backfill bool
}

func NewStore(r *storage.Recorder, c config.Config) *Store {
	return &Store{Recorder: r, Config: c, Target: VMTarget(c.Outputs.VictoriaMetrics)}
}
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func VMTarget(v config.VM) string { return Hash([]string{v.ImportEndpoint, v.MetricsPrefix}) }

type VMLine struct {
	Metric     map[string]string `json:"metric"`
	Values     []float64         `json:"values"`
	Timestamps []int64           `json:"timestamps"`
}

func Metric(d config.Device, e domain.Event, r domain.Reading, prefix string) map[string]string {
	if prefix != "" && !strings.HasSuffix(prefix, "_") {
		prefix += "_"
	}
	mac := strings.ToUpper(strings.ReplaceAll(d.MAC, ":", ""))
	if mac == "" {
		mac = d.ID
	}
	model := e.Model
	if model == "" {
		model = d.Model
	}
	if model == "" {
		model = "UNKNOWN"
	}
	return map[string]string{"__name__": prefix + r.Sensor, "device_mac": mac, "device_name": d.Alias, "sensor": r.Sensor, "unit": r.Unit, "device_model": model, "device_protocol": e.Protocol}
}
func Allowed(model, sensor string) bool {
	p := device.Lookup(model)
	return len(p.Sensors) == 0 || slices.Contains(p.Sensors, sensor)
}
func GoodQuality(q string) bool { return q == "normal" || q == "ok" || q == "higher" || q == "lower" }
func (s *Store) Reject(ctx context.Context, tx *sql.Tx, identity, reason string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO output_rejections(target,identity,reason,payload,created) VALUES(?,?,?,?,?)`, s.Target, identity, reason, string(b), time.Now().Unix())
	return err
}

func PrepareVM(v config.VM, d config.Device, e domain.Event, sm domain.Sample, r domain.Reading) (VMLine, string, string, error) {
	if r.OriginalValue != nil {
		r.Value = *r.OriginalValue
		r.Unit = r.OriginalUnit
	}
	line := VMLine{Metric: Metric(d, e, r, v.MetricsPrefix), Values: []float64{r.Value}, Timestamps: []int64{sm.Timestamp * 1000}}
	identity := Hash(struct {
		Metric    map[string]string
		Timestamp int64
	}{line.Metric, sm.Timestamp})
	reason := ""
	switch {
	case math.IsNaN(r.Value) || math.IsInf(r.Value, 0):
		return line, identity, "", fmt.Errorf("nonfinite %s reading", r.Sensor)
	case sm.Timestamp > math.MaxInt64/1000 || sm.Timestamp < math.MinInt64/1000:
		reason = "timestamp-overflow"
	case !v.IncludeInvalidTime && domain.TimeQuality(sm.Timestamp, e.ReceivedAt) != "device":
		reason = "invalid-time"
	case v.NormalOnly && !GoodQuality(r.Quality):
		reason = "quality-" + r.Quality
	}

	return line, identity, reason, nil
}

// EnqueueVM freezes labels and values at capture time. First accepted value wins
// a timestamp collision; the conflicting evidence is retained separately.
func (s *Store) EnqueueVM(ctx context.Context, tx *sql.Tx, d config.Device, e domain.Event, sm domain.Sample, r domain.Reading) error {
	if !Allowed(e.Model, r.Sensor) {
		return nil
	}
	line, identity, reason, err := PrepareVM(s.Config.Outputs.VictoriaMetrics, d, e, sm, r)
	if err != nil {
		return err
	}
	r.Value = line.Values[0]
	if reason != "" {
		return s.Reject(ctx, tx, identity, reason, line)
	}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	priority := 0
	if s.Backfill {
		priority = 1
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO vm_samples(target,identity,timestamp,value,payload,created,priority) VALUES(?,?,?,?,?,?,?)`, s.Target, identity, sm.Timestamp, r.Value, string(b), time.Now().Unix(), priority)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		return nil
	}
	var value float64
	if err = tx.QueryRowContext(ctx, `SELECT value FROM vm_samples WHERE target=? AND identity=?`, s.Target, identity).Scan(&value); err != nil {
		return err
	}
	if value != r.Value {
		return s.Reject(ctx, tx, identity, "conflicting-value", line)
	}
	return nil
}

// CaptureHook is executed inside the same transaction as capture/readings.
func (s *Store) CaptureHook(ctx context.Context, e domain.Event) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		if e.Direction != domain.FromDevice || e.Error != "" {
			return nil
		}
		var d config.Device
		found := false
		for _, candidate := range s.Config.Devices {
			if candidate.ID == e.DeviceID {
				d = candidate
				found = true
				break
			}
		}
		if !found || !d.Active() {
			return nil
		}
		for _, sm := range e.Samples {
			for _, r := range sm.Readings {
				if s.Config.Outputs.VictoriaMetrics.Enabled {
					if err := s.EnqueueVM(ctx, tx, d, e, sm, r); err != nil {
						return err
					}
				}
				if s.Config.Outputs.HomeAssistant.Enabled && d.HAActive() && Allowed(e.Model, r.Sensor) && domain.TimeQuality(sm.Timestamp, e.ReceivedAt) == "device" {
					b, err := json.Marshal(r)
					if err != nil {
						return err
					}
					// Equal timestamps never overwrite a previous reading; late history cannot regress state.
					_, err = tx.ExecContext(ctx, `INSERT INTO ha_state(device_id,sensor,timestamp,reading) VALUES(?,?,?,?) ON CONFLICT(device_id,sensor) DO UPDATE SET timestamp=excluded.timestamp,reading=excluded.reading WHERE excluded.timestamp>ha_state.timestamp`, d.ID, r.Sensor, sm.Timestamp, string(b))
					if err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
}
func (s *Store) UpdateStats(ctx context.Context, stats *Stats) error {
	var n, oldest, rejected int64
	err := s.Recorder.Database().QueryRowContext(ctx, `SELECT count(*),coalesce(min(created),0) FROM vm_samples WHERE target=? AND delivered=0`, s.Target).Scan(&n, &oldest)
	if err != nil {
		return err
	}
	if err = s.Recorder.Database().QueryRowContext(ctx, `SELECT count(*) FROM output_rejections`).Scan(&rejected); err != nil {
		return err
	}
	stats.VMPending.Store(n)
	if n > 0 {
		oldest = time.Now().Unix() - oldest
	}
	stats.VMOldestSeconds.Store(oldest)
	stats.Rejected.Store(rejected)
	stats.StorageBytes.Store(s.Recorder.Size())
	return nil
}

// DeliveryStatus persists sanitized transport diagnostics for offline inspection.
func (s *Store) DeliveryStatus(ctx context.Context, target string, err error) {
	now := time.Now().Unix()
	if err != nil {
		_, _ = s.Recorder.Database().ExecContext(ctx, `INSERT INTO output_status(target,last_error_at,last_error) VALUES(?,?,?) ON CONFLICT(target) DO UPDATE SET last_error_at=excluded.last_error_at,last_error=excluded.last_error`, target, now, err.Error())
	} else {
		_, _ = s.Recorder.Database().ExecContext(ctx, `INSERT INTO output_status(target,last_success) VALUES(?,?) ON CONFLICT(target) DO UPDATE SET last_success=excluded.last_success,last_error=''`, target, now)
	}
}

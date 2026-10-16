package doctor

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/output"
	"github.com/danielskowronski/qiotlctl/internal/storage"
)

func inspectStorage(ctx context.Context, c config.Config, r *diagnostic.Registry) []output.HAState {
	if c.Outputs.VictoriaMetrics.Enabled {
		r.Set("vm.delivery", diagnostic.Unknown, "delivery history not yet inspected", "use --daemon-url or an accessible journal", nil)
		r.Set("vm.backlog", diagnostic.Unknown, "queue not yet inspected", "use --daemon-url or an accessible journal", nil)
	}
	if c.Storage.Path == "" {
		r.Set("storage", diagnostic.Disabled, "recording disabled", "", nil)
		return nil
	}
	if _, err := os.Stat(c.Storage.Path); err != nil {
		r.Set("storage", diagnostic.Warning, "configured journal is unavailable locally; no file created", "use --daemon-url for the running daemon's storage/output status", nil)
		return nil
	}
	db, err := storage.OpenReadOnly(c.Storage.Path)
	if err != nil {
		r.Set("storage", diagnostic.Failed, "cannot open journal read-only", "check path/permissions and schema; doctor never creates or migrates a database", nil)
		return nil
	}
	defer db.Close()
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		r.Set("storage", diagnostic.Failed, "journal is not a supported Go output schema", "use the daemon journal, not the Python archive", nil)
		return nil
	}
	r.Set("storage", diagnostic.OK, "journal opened read-only; live writeability is not tested", "", nil)
	if c.Outputs.VictoriaMetrics.Enabled {
		var count, oldest int64
		err = db.QueryRowContext(ctx, `SELECT count(*),coalesce(min(created),0) FROM vm_samples WHERE target=? AND delivered=0`, output.VMTarget(c.Outputs.VictoriaMetrics)).Scan(&count, &oldest)
		if err != nil {
			r.Set("vm.backlog", diagnostic.Failed, "cannot read VM queue", "check journal schema/integrity", nil)
		} else {
			age := int64(0)
			if count > 0 {
				age = max(0, time.Now().Unix()-oldest)
			}
			status := diagnostic.OK
			reason := "queue within age limit"
			if time.Duration(age)*time.Second > c.Health.MaxPendingAge.Duration() {
				status = diagnostic.Failed
				reason = "oldest pending VM sample exceeds health.max_pending_age"
			}
			r.Set("vm.backlog", status, reason, "", map[string]int64{"pending": count, "oldest_pending_seconds": age})
		}
		var success, failure int64
		var message string
		err = db.QueryRowContext(ctx, `SELECT last_success,last_error_at,last_error FROM output_status WHERE target=?`, output.VMTarget(c.Outputs.VictoriaMetrics)).Scan(&success, &failure, &message)
		switch {
		case err == sql.ErrNoRows:
			r.Set("vm.delivery", diagnostic.Warning, "no recorded sensor delivery attempt", "waiting for real measurements", nil)
		case err != nil:
			r.Set("vm.delivery", diagnostic.Failed, "cannot read delivery history", "check journal health", nil)
		case message != "":
			r.Set("vm.delivery", diagnostic.Failed, "last sensor delivery failed", "inspect daemon diagnostics; endpoint probes cannot clear a batch failure", map[string]int64{"last_success_unix": success, "last_error_unix": failure})
		default:
			r.Set("vm.delivery", diagnostic.OK, "journal records a successful sensor delivery", "historical success is not proof of current remote query results", map[string]int64{"last_success_unix": success})
		}
	}
	var rejected int64
	if db.QueryRowContext(ctx, "SELECT count(*) FROM output_rejections").Scan(&rejected) == nil && rejected > 0 {
		r.Set("samples.rejected", diagnostic.Warning, "journal contains rejected/conflicting samples", "inspect outputs rejections for invalid clock/quality/conflicts", map[string]int64{"samples": rejected})
	}
	rows, err := db.QueryContext(ctx, "SELECT device_id,timestamp,reading FROM ha_state")
	if err != nil {
		r.Set("storage", diagnostic.Failed, "cannot read HA state journal", "check journal schema/integrity", nil)
		return nil
	}
	defer rows.Close()
	var states []output.HAState
	for rows.Next() {
		var s output.HAState
		var b []byte
		if rows.Scan(&s.DeviceID, &s.Timestamp, &b) != nil || json.Unmarshal(b, &s.Reading) != nil {
			r.Set("storage", diagnostic.Failed, "cannot decode HA state journal", "check journal integrity", nil)
			return nil
		}
		states = append(states, s)
	}
	if rows.Err() != nil {
		r.Set("storage", diagnostic.Failed, "cannot finish journal inspection", "check journal health and doctor timeout", nil)
	}
	return states
}

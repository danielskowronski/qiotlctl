package diagnostic

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type logged struct {
	key string
	at  time.Time
}

// Reporter is owned by one goroutine; the registry is safe for concurrent workers.
type Reporter struct {
	Logger   *slog.Logger
	Interval time.Duration
	previous map[string]logged
}

func (r *Reporter) Emit(s Snapshot) {
	if r.previous == nil {
		r.previous = map[string]logged{}
	}
	for _, c := range s.Checks {
		key := fmt.Sprintf("%s:%t:%s:%s", c.Status, c.Blocking, c.Reason, c.Remedy)
		// Successful probes/batches can have different evidence text; log only
		// their status transition, not every successful refresh.
		if c.Status == OK || c.Status == Disabled {
			key = string(c.Status)
		}
		old, exists := r.previous[c.ID]
		if exists && key == old.key && (c.Status == OK || c.Status == Disabled || s.GeneratedAt.Sub(old.at) < r.Interval) {
			continue
		}
		level := slog.LevelInfo
		if c.Status == Failed {
			level = slog.LevelError
		} else if c.Status == Warning {
			level = slog.LevelWarn
		}
		message := "health check"
		if exists && old.key != key && c.Status == OK {
			message = "health recovered"
		}
		args := []any{"check", c.ID, "status", c.Status, "blocking", c.Blocking, "reason", c.Reason}
		if c.Remedy != "" {
			args = append(args, "remedy", c.Remedy)
		}
		if len(c.Counts) > 0 {
			args = append(args, "counts", c.Counts)
		}
		r.Logger.Log(context.Background(), level, message, args...)
		r.previous[c.ID] = logged{key, s.GeneratedAt}
	}
}
func (r *Reporter) Run(ctx context.Context, registry *Registry) {
	r.Emit(registry.Snapshot(time.Now()))
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.Emit(registry.Snapshot(now))
		}
	}
}

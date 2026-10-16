// Package diagnostic holds shared, cached operational evidence. It performs no I/O.
package diagnostic

import (
	"sort"
	"sync"
	"time"
)

type Status string

const (
	OK       Status = "ok"
	Warning  Status = "warning"
	Failed   Status = "failed"
	Unknown  Status = "unknown"
	Disabled Status = "disabled"
)

type Check struct {
	ID           string           `json:"id"`
	Status       Status           `json:"status"`
	Required     bool             `json:"required"`
	Blocking     bool             `json:"blocking"`
	Reason       string           `json:"reason"`
	Remedy       string           `json:"remedy,omitempty"`
	CheckedAt    time.Time        `json:"checked_at"`
	LastSuccess  time.Time        `json:"last_success"`
	LastFailure  time.Time        `json:"last_failure"`
	FailureSince time.Time        `json:"failure_since"`
	Counts       map[string]int64 `json:"counts,omitempty"`
}
type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Ready         bool      `json:"ready"`
	Checks        []Check   `json:"checks"`
}
type entry struct {
	Check
	grace, maxAge time.Duration
}
type Registry struct {
	mu     sync.RWMutex
	checks map[string]entry
}

func New() *Registry { return &Registry{checks: map[string]entry{}} }
func (r *Registry) Register(id string, required bool, grace, maxAge time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks[id] = entry{Check: Check{ID: id, Required: required, Status: Unknown, Reason: "waiting for first check"}, grace: grace, maxAge: maxAge}
}
func copyCounts(m map[string]int64) map[string]int64 {
	if m == nil {
		return nil
	}
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
func (r *Registry) Set(id string, status Status, reason, remedy string, counts map[string]int64) {
	r.SetAt(id, status, reason, remedy, counts, time.Now())
}
func (r *Registry) SetAt(id string, status Status, reason, remedy string, counts map[string]int64, now time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.checks[id]
	e.ID = id
	if status == Failed {
		if e.Status != Failed {
			e.FailureSince = now
		}
		e.LastFailure = now
	} else {
		e.FailureSince = time.Time{}
	}
	if status == OK {
		e.LastSuccess = now
	}
	e.Status = status
	e.Reason = reason
	e.Remedy = remedy
	e.CheckedAt = now
	e.Counts = copyCounts(counts)
	r.checks[id] = e
}
func (r *Registry) Snapshot(now time.Time) Snapshot {
	s := Snapshot{SchemaVersion: 1, GeneratedAt: now, Ready: true, Checks: []Check{}}
	if r == nil {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.checks {
		c := e.Check
		c.Counts = copyCounts(c.Counts)
		if e.maxAge > 0 && c.Status == OK && now.Sub(c.CheckedAt) > e.maxAge {
			c.Status = Unknown
			c.Reason = "health evidence expired"
			c.Remedy = "check worker progress and connectivity"
		}
		c.Blocking = c.Required && (c.Status == Failed || c.Status == Unknown)
		if c.Status == Failed && !c.LastSuccess.IsZero() && now.Sub(c.FailureSince) < e.grace {
			c.Blocking = false
		}
		if c.Blocking {
			s.Ready = false
		}
		s.Checks = append(s.Checks, c)
	}
	sort.Slice(s.Checks, func(i, j int) bool { return s.Checks[i].ID < s.Checks[j].ID })
	return s
}

package output

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/config"
	"github.com/danielskowronski/qiotlctl/internal/diagnostic"
	"github.com/danielskowronski/qiotlctl/internal/transport"
)

// ExpectedDiscovery includes observed sensors for generic models.
func ExpectedDiscovery(c config.Config, states []HAState) []Publication {
	byTopic := map[string]Publication{}
	for _, p := range Preview(c) {
		byTopic[p.Topic] = p
	}
	for _, d := range c.Devices {
		if !d.HAActive() {
			continue
		}
		for _, state := range states {
			if state.DeviceID == d.ID && Allowed(d.Model, state.Reading.Sensor) {
				if p, ok := Discovery(c, d, state.Reading.Sensor); ok {
					byTopic[p.Topic] = p
				}
			}
		}
	}
	var out []Publication
	for _, p := range byTopic {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out
}
func jsonEqual(a, b []byte) bool {
	var x, y any
	da, db := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	da.UseNumber()
	db.UseNumber()
	return da.Decode(&x) == nil && db.Decode(&y) == nil && reflect.DeepEqual(x, y) && da.Decode(new(any)) == io.EOF && db.Decode(new(any)) == io.EOF
}
func CheckDiscovery(expected []Publication, messages map[string]transport.Observed) (diagnostic.Status, string, map[string]int64) {
	counts := map[string]int64{"expected": int64(len(expected)), "matched": 0, "missing": 0, "mismatched": 0}
	first := ""
	for _, p := range expected {
		m, ok := messages[p.Topic]
		if !ok || !m.Retained {
			counts["missing"]++
			if first == "" {
				first = "retained discovery not observed at " + p.Topic
			}
			continue
		}
		if !jsonEqual(m.Payload, p.Payload) {
			counts["mismatched"]++
			if first == "" {
				first = "retained discovery differs at " + p.Topic
			}
			continue
		}
		counts["matched"]++
	}
	if first != "" {
		return diagnostic.Failed, first, counts
	}
	if len(expected) == 0 {
		return diagnostic.Warning, "no known HA sensors yet; waiting for supported readings", counts
	}
	return diagnostic.OK, "expected retained discovery verified on broker; HA entity acceptance is separate", counts
}
func InspectDiscovery(ctx context.Context, c config.Config, expected []Publication, r *diagnostic.Registry) bool {
	topics := make([]string, 0, len(expected))
	for _, p := range expected {
		topics = append(topics, p.Topic)
	}
	if len(topics) == 0 {
		r.Set("ha.discovery", diagnostic.Warning, "no known HA sensors yet", "wait for supported readings", nil)
		return true
	}
	messages, err := transport.Inspect(ctx, c.MQTT, topics, "")
	if err != nil {
		r.Set("ha.discovery", diagnostic.Failed, err.Error(), "allow qiotld to subscribe to its discovery topics for verification", nil)
		return false
	}
	status, reason, counts := CheckDiscovery(expected, messages)
	r.Set("ha.discovery", status, reason, "check discovery publish/subscribe/retain ACLs and configured HA IDs", counts)
	return status != diagnostic.Failed
}
func (s *Store) CheckBacklog(ctx context.Context, stats *Stats) bool {
	if err := s.UpdateStats(ctx, stats); err != nil {
		stats.Diagnostics.Set("vm.backlog", diagnostic.Failed, "cannot inspect output queue", "check capture database health", nil)
		return false
	}
	counts := map[string]int64{"pending": stats.VMPending.Load(), "oldest_pending_seconds": stats.VMOldestSeconds.Load()}
	status := diagnostic.OK
	reason := "queue within age limit"
	if time.Duration(counts["oldest_pending_seconds"])*time.Second > s.Config.Health.MaxPendingAge.Duration() {
		status = diagnostic.Failed
		reason = "oldest pending VM sample exceeds health.max_pending_age"
	}
	stats.Diagnostics.Set("vm.backlog", status, reason, "check VM delivery and endpoint capacity; queued work is preserved", counts)
	return status == diagnostic.OK
}
func HTTPReason(code int) string {
	switch code {
	case 401, 403:
		return fmt.Sprintf("HTTP %d: VM authentication/authorization rejected", code)
	case 404:
		return "HTTP 404: VM import route not found"
	case 429:
		return "HTTP 429: VM rate limit reached"
	}
	return fmt.Sprintf("VM import returned HTTP %d", code)
}

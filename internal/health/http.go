// Package health serves process probes and bounded-cardinality operational metrics.
package health

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/danielskowronski/qiotlctl/internal/observer"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func Handler(s *observer.Stats) http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	counters := []struct {
		name, help string
		value      *atomic.Uint64
	}{
		{"vm_samples_delivered_total", "VM samples accepted by the import endpoint.", &s.Outputs.VMSent},
		{"vm_errors_total", "VM retryable delivery failures.", &s.Outputs.VMErrors},
		{"ha_publications_total", "HA MQTT publications accepted by the broker.", &s.Outputs.HAPublished},
		{"ha_errors_total", "HA delivery failures.", &s.Outputs.HAErrors},
		{"messages_received_total", "MQTT messages received.", &s.Received},
		{"messages_decoded_total", "Successfully decoded messages.", &s.Decoded},
		{"decode_errors_total", "Messages that could not be decoded.", &s.DecodeErrors},
		{"samples_total", "Decoded samples, including retransmitted history.", &s.Samples},
		{"readings_total", "Decoded individual sensor readings.", &s.Readings},
		{"captures_recorded_total", "Durably recorded captures.", &s.Recorded},
		{"queue_overflows_total", "Queue overflows that stop the observer.", &s.QueueOverflows},
		{"replies_published_total", "Automatic application replies published.", &s.Replies},
		{"reply_errors_total", "Automatic replies with uncertain delivery.", &s.ReplyErrors},
		{"clock_anomalies_total", "Observed samples with pre-2020 or future device timestamps.", &s.ClockAnomalies},
		{"connections_total", "Successful MQTT connections, including reconnects.", &s.Reconnects},
	}
	for _, c := range counters {
		registry.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Namespace: "qiotl", Name: c.name, Help: c.help}, func() float64 { return float64(c.value.Load()) }))
	}
	bools := []struct {
		name, help string
		value      func() bool
	}{
		{"vm_healthy", "VM last delivery attempt succeeded (output must be enabled).", s.Outputs.VMHealthy.Load},
		{"ha_healthy", "HA last synchronization succeeded (output must be enabled).", s.Outputs.HAHealthy.Load},
		{"mqtt_connected", "MQTT connection is active.", s.Connected.Load},
		{"mqtt_subscribed", "All configured subscriptions are accepted.", s.Subscribed.Load},
		{"storage_ready", "Configured recorder is available.", s.StorageOK.Load},
		{"ready", "Observer is ready.", s.Ready},
	}
	for _, g := range bools {
		registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "qiotl", Name: g.name, Help: g.help}, func() float64 {
			if g.value() {
				return 1
			}
			return 0
		}))
	}
	registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "qiotl", Name: "queue_depth", Help: "Pending captures in memory."}, func() float64 { return float64(s.QueueDepth.Load()) }))
	for _, g := range []struct {
		name, help string
		value      *atomic.Int64
	}{
		{"vm_pending_samples", "Undelivered samples for the active VM destination.", &s.Outputs.VMPending},
		{"vm_oldest_pending_seconds", "Age of oldest pending sample.", &s.Outputs.VMOldestSeconds},
		{"output_rejections", "Persisted invalid or conflicting samples.", &s.Outputs.Rejected},
		{"storage_bytes", "Recorder database, WAL and SHM bytes.", &s.Outputs.StorageBytes},
	} {
		registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "qiotl", Name: g.name, Help: g.help}, func() float64 { return float64(g.value.Load()) }))
	}
	// IDs come from the fixed component registry, never device IDs or raw topics.
	for _, check := range s.Diagnostics.Snapshot(time.Now()).Checks {
		id := check.ID
		registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "qiotl", Name: "health_check_blocking", Help: "Required health check currently blocks readiness.", ConstLabels: prometheus.Labels{"check": id}}, func() float64 {
			for _, c := range s.Diagnostics.Snapshot(time.Now()).Checks {
				if c.ID == id && c.Blocking {
					return 1
				}
			}
			return 0
		}))
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		snapshot := s.Diagnostics.Snapshot(time.Now())
		snapshot.Ready = snapshot.Ready && s.CaptureReady()
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if !s.Ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready\n"))
			for _, c := range s.Diagnostics.Snapshot(time.Now()).Checks {
				if c.Blocking {
					_, _ = fmt.Fprintf(w, "%s: %s", c.ID, c.Reason)
					if c.Remedy != "" {
						_, _ = fmt.Fprintf(w, "; %s", c.Remedy)
					}
					_, _ = fmt.Fprintln(w)
				}
			}
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	return mux
}

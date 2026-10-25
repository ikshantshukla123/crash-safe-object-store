// Package metrics is a small, dependency-free registry shared by the API and
// workers. It exposes both a JSON snapshot for the dashboard and Prometheus
// text for scraping.
package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// Registry holds process counters. Zero value is not usable; use New.
type Registry struct {
	startedAt time.Time

	ObjectWrites     atomic.Int64
	ObjectReads      atomic.Int64
	CacheHits        atomic.Int64
	CacheMisses      atomic.Int64
	ReplicaRepairs   atomic.Int64
	ObjectsRecovered atomic.Int64
	RepairFailures   atomic.Int64
	ScrubFailures    atomic.Int64
	DataLossAlerts   atomic.Int64
	EmbeddingsDone   atomic.Int64
	ChaosActions     atomic.Int64
	RepairLatencyMs  atomic.Int64
	LastRepairUnix   atomic.Int64
}

// New returns a registry with the process start time set to now.
func New() *Registry {
	return &Registry{startedAt: time.Now()}
}

// Snapshot is the dashboard-facing view of the counters.
type Snapshot struct {
	UptimeSeconds    int64 `json:"uptime_seconds"`
	ObjectWrites     int64 `json:"object_writes"`
	ObjectReads      int64 `json:"object_reads"`
	CacheHits        int64 `json:"cache_hits"`
	CacheMisses      int64 `json:"cache_misses"`
	ReplicaRepairs   int64 `json:"replica_repairs"`
	ObjectsRecovered int64 `json:"objects_recovered"`
	RepairFailures   int64 `json:"repair_failures"`
	ScrubFailures    int64 `json:"scrub_failures"`
	DataLossAlerts   int64 `json:"data_loss_alerts"`
	EmbeddingsDone   int64 `json:"embeddings_done"`
	ChaosActions     int64 `json:"chaos_actions"`
	RepairLatencyMs  int64 `json:"repair_latency_ms"`
	LastRepairUnix   int64 `json:"last_repair_unix"`
}

// Snapshot copies the counters atomically enough for a live dashboard.
func (r *Registry) Snapshot() Snapshot {
	return Snapshot{
		UptimeSeconds:    int64(time.Since(r.startedAt).Seconds()),
		ObjectWrites:     r.ObjectWrites.Load(),
		ObjectReads:      r.ObjectReads.Load(),
		CacheHits:        r.CacheHits.Load(),
		CacheMisses:      r.CacheMisses.Load(),
		ReplicaRepairs:   r.ReplicaRepairs.Load(),
		ObjectsRecovered: r.ObjectsRecovered.Load(),
		RepairFailures:   r.RepairFailures.Load(),
		ScrubFailures:    r.ScrubFailures.Load(),
		DataLossAlerts:   r.DataLossAlerts.Load(),
		EmbeddingsDone:   r.EmbeddingsDone.Load(),
		ChaosActions:     r.ChaosActions.Load(),
		RepairLatencyMs:  r.RepairLatencyMs.Load(),
		LastRepairUnix:   r.LastRepairUnix.Load(),
	}
}

// ObserveRepair records a successful repair and how long it took.
func (r *Registry) ObserveRepair(d time.Duration) {
	r.ReplicaRepairs.Add(1)
	r.ObjectsRecovered.Add(1)
	r.RepairLatencyMs.Store(d.Milliseconds())
	r.LastRepairUnix.Store(time.Now().Unix())
}

var prometheusCounters = []struct {
	name string
	get  func(*Registry) int64
}{
	{"objstore_object_writes_total", func(r *Registry) int64 { return r.ObjectWrites.Load() }},
	{"objstore_object_reads_total", func(r *Registry) int64 { return r.ObjectReads.Load() }},
	{"objstore_cache_hits_total", func(r *Registry) int64 { return r.CacheHits.Load() }},
	{"objstore_cache_misses_total", func(r *Registry) int64 { return r.CacheMisses.Load() }},
	{"objstore_replica_repairs_total", func(r *Registry) int64 { return r.ReplicaRepairs.Load() }},
	{"objstore_objects_recovered_total", func(r *Registry) int64 { return r.ObjectsRecovered.Load() }},
	{"objstore_repair_failures_total", func(r *Registry) int64 { return r.RepairFailures.Load() }},
	{"objstore_scrub_failures_total", func(r *Registry) int64 { return r.ScrubFailures.Load() }},
	{"objstore_data_loss_alerts_total", func(r *Registry) int64 { return r.DataLossAlerts.Load() }},
	{"objstore_embeddings_total", func(r *Registry) int64 { return r.EmbeddingsDone.Load() }},
	{"objstore_chaos_actions_total", func(r *Registry) int64 { return r.ChaosActions.Load() }},
}

// WritePrometheus emits the text exposition format without pulling in a
// client library just to print counters.
func (r *Registry) WritePrometheus(w io.Writer) {
	for _, c := range prometheusCounters {
		_, _ = fmt.Fprintf(w, "# TYPE %s counter\n%s %d\n", c.name, c.name, c.get(r))
	}
	_, _ = fmt.Fprintf(w, "# TYPE objstore_repair_latency_ms gauge\nobjstore_repair_latency_ms %d\n", r.RepairLatencyMs.Load())
	_, _ = fmt.Fprintf(w, "# TYPE objstore_uptime_seconds gauge\nobjstore_uptime_seconds %d\n", int64(time.Since(r.startedAt).Seconds()))
}

// Handler serves the Prometheus text endpoint.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.WritePrometheus(w)
	})
}

// ServeSnapshot writes the JSON snapshot.
func (r *Registry) ServeSnapshot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(r.Snapshot())
}

// SnapshotHandler serves the JSON snapshot.
func (r *Registry) SnapshotHandler() http.Handler {
	return http.HandlerFunc(r.ServeSnapshot)
}

// ParseInt is a small helper for dashboard query parameters.
func ParseInt(raw string, def int64) int64 {
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return v
}

package api

import (
	"fmt"
	"io"
	"runtime"
	"sort"
	"strconv"
	"sync"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor/rego"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
)

// metricsCounter tracks HTTP request counts by (method, status). It's
// the only counter the Phase 1 /metrics endpoint emits beyond
// goroutine count and informer-sync state. A sync.Mutex is plenty for
// the request volumes Phase 1 targets (no need for sharded counters).
type metricsCounter struct {
	mu     sync.Mutex
	counts map[counterKey]uint64
}

type counterKey struct {
	method string
	status int
}

func newMetricsCounter() *metricsCounter {
	return &metricsCounter{counts: make(map[counterKey]uint64)}
}

func (m *metricsCounter) inc(method string, status int) {
	m.mu.Lock()
	m.counts[counterKey{method: method, status: status}]++
	m.mu.Unlock()
}

// snapshot copies the counter map so write doesn't block on the
// caller's serialisation.
func (m *metricsCounter) snapshot() map[counterKey]uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[counterKey]uint64, len(m.counts))
	for k, v := range m.counts {
		out[k] = v
	}
	return out
}

// writePrometheus emits the /metrics body in the Prometheus text
// exposition format. The metric set is intentionally tiny: a single
// gauge per dimension Phase 1 cares about. We hand-roll the format
// to avoid pulling in prometheus/client_golang's registry just for
// three numbers.
//
// Write errors are ignored: a hung-up scraper isn't something /metrics
// can do anything useful about.
func writePrometheus(w io.Writer, gate *ReadinessGate, counter *metricsCounter, regoMetrics *rego.Metrics, regoModules func() int, snapMetrics *snapshot.Metrics, snapQueueDepth func() int, dynMetrics *discovery.DynamicMetrics) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

	p("# HELP kubeatlas_goroutines Number of currently running goroutines.\n")
	p("# TYPE kubeatlas_goroutines gauge\n")
	p("kubeatlas_goroutines %d\n", runtime.NumGoroutine())

	p("# HELP kubeatlas_informer_synced 1 if the informer cache has completed initial sync, 0 otherwise.\n")
	p("# TYPE kubeatlas_informer_synced gauge\n")
	synced := 0
	if gate != nil && gate.IsReady() {
		synced = 1
	}
	p("kubeatlas_informer_synced %d\n", synced)

	p("# HELP kubeatlas_api_requests_total Total HTTP requests served, broken down by method and status.\n")
	p("# TYPE kubeatlas_api_requests_total counter\n")
	if counter != nil {
		snap := counter.snapshot()
		// Sort for deterministic output — easier on humans and on
		// scrape diffs.
		keys := make([]counterKey, 0, len(snap))
		for k := range snap {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].method != keys[j].method {
				return keys[i].method < keys[j].method
			}
			return keys[i].status < keys[j].status
		})
		for _, k := range keys {
			p("kubeatlas_api_requests_total{method=%q,status=%q} %d\n",
				k.method, strconv.Itoa(k.status), snap[k])
		}
	}

	// Phase 2 rego engine block — emitted only when main.go wired a
	// metrics provider via WithRegoMetrics. The /metrics scrape
	// stays Phase-1-shaped on a build that never spawned an engine
	// (e.g. -once mode, tests).
	if regoModules != nil {
		p("# HELP kubeatlas_rego_modules_loaded Number of compiled Rego modules currently registered in the engine.\n")
		p("# TYPE kubeatlas_rego_modules_loaded gauge\n")
		p("kubeatlas_rego_modules_loaded %d\n", regoModules())
	}
	if regoMetrics != nil {
		s := regoMetrics.Snapshot()
		p("# HELP kubeatlas_rego_cache_hits_total Cached rego evaluations served without re-running OPA.\n")
		p("# TYPE kubeatlas_rego_cache_hits_total counter\n")
		p("kubeatlas_rego_cache_hits_total %d\n", s.CacheHits)
		p("# HELP kubeatlas_rego_cache_misses_total Rego evaluations that fell through to OPA.\n")
		p("# TYPE kubeatlas_rego_cache_misses_total counter\n")
		p("kubeatlas_rego_cache_misses_total %d\n", s.CacheMisses)
		p("# HELP kubeatlas_rego_eval_timeout_total Rego evaluations aborted by the per-call timeout.\n")
		p("# TYPE kubeatlas_rego_eval_timeout_total counter\n")
		p("kubeatlas_rego_eval_timeout_total %d\n", s.EvalTimeouts)
		p("# HELP kubeatlas_rego_eval_panic_total Rego evaluations whose underlying OPA call panicked.\n")
		p("# TYPE kubeatlas_rego_eval_panic_total counter\n")
		p("kubeatlas_rego_eval_panic_total %d\n", s.EvalPanics)
	}

	// Phase 3 F-111 snapshot writer block — emitted only when main.go
	// wired WithSnapshotMetrics, i.e. on a Tier 2 install with
	// snapshots.enabled. A Tier 1 / snapshots-off scrape stays free
	// of this block.
	if snapMetrics != nil {
		s := snapMetrics.Snapshot()
		p("# HELP kubeatlas_snapshot_events_processed_total Resource events durably written to the snapshot stream.\n")
		p("# TYPE kubeatlas_snapshot_events_processed_total counter\n")
		p("kubeatlas_snapshot_events_processed_total %d\n", s.EventsProcessed)
		p("# HELP kubeatlas_snapshot_write_failed_total Events dropped after the per-event retry budget was exhausted.\n")
		p("# TYPE kubeatlas_snapshot_write_failed_total counter\n")
		p("kubeatlas_snapshot_write_failed_total %d\n", s.WriteFailed)
		p("# HELP kubeatlas_snapshot_queue_drop_total Events dropped at enqueue because the writer queue was full.\n")
		p("# TYPE kubeatlas_snapshot_queue_drop_total counter\n")
		p("kubeatlas_snapshot_queue_drop_total %d\n", s.QueueDropped)
	}
	if snapQueueDepth != nil {
		p("# HELP kubeatlas_snapshot_queue_depth Events currently buffered in the snapshot writer queue.\n")
		p("# TYPE kubeatlas_snapshot_queue_depth gauge\n")
		p("kubeatlas_snapshot_queue_depth %d\n", snapQueueDepth())
	}

	// Phase 4 dynamic informer block — emitted only when main.go wired
	// WithDynamicInformerMetrics (single-cluster mode with the
	// Gatekeeper component running).
	if dynMetrics != nil {
		s := dynMetrics.Snapshot()
		p("# HELP kubeatlas_dynamic_informer_active_total Dynamic informers currently running (e.g. one per Gatekeeper Constraint kind).\n")
		p("# TYPE kubeatlas_dynamic_informer_active_total gauge\n")
		p("kubeatlas_dynamic_informer_active_total %d\n", s.Active)
		p("# HELP kubeatlas_dynamic_informer_errors_total Errors registering dynamic informers.\n")
		p("# TYPE kubeatlas_dynamic_informer_errors_total counter\n")
		p("kubeatlas_dynamic_informer_errors_total %d\n", s.Errors)
	}
}

package api

import (
	"fmt"
	"io"
	"runtime"
	"sort"
	"strconv"
	"sync"
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
func writePrometheus(w io.Writer, gate *ReadinessGate, counter *metricsCounter) {
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
}

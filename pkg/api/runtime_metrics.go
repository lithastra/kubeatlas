// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"fmt"
	"io"
	"runtime/metrics"
)

var runtimeMemoryMetrics = [...]struct {
	name, runtimeName, kind, help string
}{
	{"kubeatlas_go_heap_alloc_bytes", "/memory/classes/heap/objects:bytes", "gauge", "Heap bytes occupied by live or not-yet-collected objects."},
	{"kubeatlas_go_heap_live_bytes", "/gc/heap/live:bytes", "gauge", "Heap bytes marked live by the previous garbage collection."},
	{"kubeatlas_go_heap_goal_bytes", "/gc/heap/goal:bytes", "gauge", "Heap size target for the end of the GC cycle."},
	{"kubeatlas_go_heap_free_bytes", "/memory/classes/heap/free:bytes", "gauge", "Free heap bytes eligible for release but not yet returned to the OS."},
	{"kubeatlas_go_heap_released_bytes", "/memory/classes/heap/released:bytes", "gauge", "Free heap bytes returned to the OS."},
	{"kubeatlas_go_heap_unused_bytes", "/memory/classes/heap/unused:bytes", "gauge", "Heap bytes reserved for objects but not currently used for objects."},
	{"kubeatlas_go_heap_stacks_bytes", "/memory/classes/heap/stacks:bytes", "gauge", "Heap bytes reserved for stack space."},
	{"kubeatlas_go_runtime_total_bytes", "/memory/classes/total:bytes", "gauge", "Read-write memory mapped by the Go runtime; not process RSS."},
	{"kubeatlas_go_gc_cycles_total", "/gc/cycles/total:gc-cycles", "counter", "Completed garbage collection cycles in this process."},
}

// writeRuntimeMemoryPrometheus observes numeric runtime counters without forcing
// GC, changing memory limits, or exposing heap contents. Scrapes own their sample
// arrays so concurrent HTTP handlers do not race on runtime/metrics.Read.
func writeRuntimeMemoryPrometheus(w io.Writer) {
	var samples [len(runtimeMemoryMetrics)]metrics.Sample
	for i, metric := range runtimeMemoryMetrics {
		samples[i].Name = metric.runtimeName
	}
	metrics.Read(samples[:])
	for i, metric := range runtimeMemoryMetrics {
		if samples[i].Value.Kind() != metrics.KindUint64 {
			// Missing runtime support must not masquerade as a zero reading.
			continue
		}
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n",
			metric.name, metric.help, metric.name, metric.kind, metric.name, samples[i].Value.Uint64())
	}
}

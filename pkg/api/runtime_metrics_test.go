// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"runtime/metrics"
	"strconv"
	"strings"
	"testing"
)

func TestRuntimeMemoryMetrics(t *testing.T) {
	available := make(map[string]metrics.Description)
	for _, desc := range metrics.All() {
		available[desc.Name] = desc
	}
	for _, metric := range runtimeMemoryMetrics {
		desc, ok := available[metric.runtimeName]
		if !ok || desc.Kind != metrics.KindUint64 {
			t.Fatalf("runtime does not support uint64 metric %s", metric.runtimeName)
		}
		if desc.Cumulative != (metric.kind == "counter") {
			t.Fatalf("incorrect Prometheus type for %s", metric.name)
		}
	}
	for range 8 {
		t.Run("concurrent scrape", func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			writeRuntimeMemoryPrometheus(&out)
			values := make(map[string]uint64)
			for _, line := range strings.Split(out.String(), "\n") {
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) != 2 {
					t.Fatalf("expected unlabeled numeric sample: %q", line)
				}
				value, err := strconv.ParseUint(fields[1], 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				if _, duplicate := values[fields[0]]; duplicate {
					t.Fatalf("duplicate metric %s", fields[0])
				}
				values[fields[0]] = value
			}
			if len(values) != len(runtimeMemoryMetrics) {
				t.Fatalf("got %d metrics, want %d", len(values), len(runtimeMemoryMetrics))
			}
			for _, metric := range runtimeMemoryMetrics {
				if _, ok := values[metric.name]; !ok {
					t.Errorf("missing %s", metric.name)
				}
				if !strings.Contains(out.String(), "# TYPE "+metric.name+" "+metric.kind+"\n") {
					t.Errorf("missing type for %s", metric.name)
				}
			}
			if values["kubeatlas_go_heap_alloc_bytes"] == 0 || values["kubeatlas_go_runtime_total_bytes"] == 0 {
				t.Fatal("allocated heap and total memory must be nonzero in this process")
			}
		})
	}
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// BenchmarkImpactAPI5K freezes a synthetic Tier 1 fixture: 1,000 ConfigMaps,
// 1,000 Deployments, 1,000 ReplicaSets, 2,000 Pods and 6,000 stored edges.
// Query: cm-0000, dependents, depth 5, limit 200, no expected UID; four matches.
// Each invocation has ten untimed warm-ups. Use -benchtime=100x -count=1
// for at least 100 measured samples. Go's initial calibration invocation is
// separate. This measures a sequential loopback HTTP GET through middleware,
// complete body receipt and decoding, not a real cluster or Tier 2 release gate.
func BenchmarkImpactAPI5K(b *testing.B) {
	benchmarkImpactAPI(b, 1000, false, 4, false)
}

// BenchmarkImpactAPI10K uses 10,000 resources and 12,000 sparse edges.
func BenchmarkImpactAPI10K(b *testing.B) {
	benchmarkImpactAPI(b, 2000, false, 4, false)
}

// BenchmarkImpactAPI5KHighFanout uses 5,000 resources and 18,000 edges.
// Every workload references the same five ConfigMaps. The root has 4,000
// reachable resources; the default response must truncate at 200 matches.
func BenchmarkImpactAPI5KHighFanout(b *testing.B) {
	benchmarkImpactAPI(b, 1000, true, 200, true)
}

func benchmarkImpactAPI(b *testing.B, groups int, shared bool, wantTotal int, wantTruncated bool) {
	ctx := context.Background()
	store := memory.New()
	resource := func(kind, name, version string) graph.Resource {
		return graph.Resource{Namespace: "bench", Kind: kind, Name: name,
			GroupVersion: version, ResourceVersion: "1"}
	}
	put := func(r graph.Resource) {
		if err := store.UpsertResource(ctx, r); err != nil {
			b.Fatal(err)
		}
	}
	edge := func(from, to graph.Resource, kind graph.EdgeType) {
		if err := store.UpsertEdge(ctx, graph.Edge{From: from.ID(), To: to.ID(), Type: kind}); err != nil {
			b.Fatal(err)
		}
	}
	for i := range groups {
		cm := resource("ConfigMap", fmt.Sprintf("cm-%04d", i), "v1")
		deployment := resource("Deployment", fmt.Sprintf("deployment-%04d", i), "apps/v1")
		rs := resource("ReplicaSet", fmt.Sprintf("rs-%04d", i), "apps/v1")
		put(cm)
		put(deployment)
		put(rs)
		configRefs := []graph.Resource{cm}
		if shared {
			configRefs = make([]graph.Resource, 5)
			for k := range configRefs {
				configRefs[k] = resource("ConfigMap", fmt.Sprintf("cm-%04d", k), "v1")
			}
		}
		for _, target := range configRefs {
			edge(deployment, target, graph.EdgeTypeUsesConfigMap)
		}
		edge(rs, deployment, graph.EdgeTypeOwns)
		for j := range 2 {
			pod := resource("Pod", fmt.Sprintf("pod-%04d-%d", i, j), "v1")
			put(pod)
			edge(pod, rs, graph.EdgeTypeOwns)
			for _, target := range configRefs {
				edge(pod, target, graph.EdgeTypeUsesConfigMap)
			}
		}
	}
	server := New("", store, aggregator.NewRegistry())
	mux := http.NewServeMux()
	server.registerRoutes(mux)
	httpServer := httptest.NewServer(chain(mux, recoveryMiddleware,
		metricsMiddleware(server.metrics), versionMetricsMiddleware(server.versionMetrics), accessLogMiddleware, corsMiddleware))
	defer httpServer.Close()
	client := httpServer.Client()
	client.Timeout = 6 * time.Second
	query := httpServer.URL + "/api/v1/impact/bench/ConfigMap/cm-0000?relation=dependents&max_depth=5&limit=200"
	get := func() {
		response, err := client.Get(query)
		if err != nil {
			b.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
			b.Fatalf("incomplete response: status=%d read=%v close=%v", response.StatusCode, readErr, closeErr)
		}
		var capture ImpactResponse
		if err := json.Unmarshal(body, &capture); err != nil {
			b.Fatal(err)
		}
		if capture.Analysis == nil || capture.Analysis.Counts.Total != wantTotal || capture.Analysis.Truncated != wantTruncated {
			b.Fatal("benchmark response does not match the frozen query")
		}
	}
	for range 10 {
		get()
	}
	samples := make([]time.Duration, b.N)
	b.ResetTimer()
	for i := range b.N {
		start := time.Now()
		get()
		samples[i] = time.Since(start)
	}
	b.StopTimer()
	slices.Sort(samples)
	b.ReportMetric(float64(samples[(95*b.N+99)/100-1])/float64(time.Millisecond), "p95-ms")
}

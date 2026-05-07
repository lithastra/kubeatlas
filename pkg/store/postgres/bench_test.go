// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Shared across benchmark invocations so Go's b.N auto-scaling does
// not re-bootstrap a container every pass. TestMain owns the
// lifetime; sync.Once gates lazy creation so tests that never need
// the bench store do not pay for it.
var (
	benchOnce   sync.Once
	benchHandle AGEHandle
	benchStore  *Store
	benchInitOK bool
)

func TestMain(m *testing.M) {
	code := m.Run()
	if benchStore != nil {
		benchStore.Close()
	}
	if benchHandle.Container != nil {
		_ = benchHandle.Container.Terminate(context.Background())
	}
	os.Exit(code)
}

// sharedBenchStore lazily boots one container/store and reuses it
// across every benchmark scaling pass in this package. Bootstrap
// errors fail the calling benchmark; subsequent calls short-circuit.
func sharedBenchStore(b *testing.B) *Store {
	b.Helper()
	benchOnce.Do(func() {
		ctx := context.Background()
		h, err := bootstrapPostgresWithAGE(ctx)
		if err != nil {
			b.Logf("sharedBenchStore: bootstrap: %v", err)
			return
		}
		s, err := New(ctx, Config{DSN: h.ConnStr})
		if err != nil {
			_ = h.Container.Terminate(context.Background())
			b.Logf("sharedBenchStore: New: %v", err)
			return
		}
		benchHandle, benchStore, benchInitOK = h, s, true
	})
	if !benchInitOK {
		b.Fatal("shared bench store unavailable; check earlier log output")
	}
	return benchStore
}

// BenchmarkListOutgoing_AGE_vs_SQL is the P2-T4 acceptance bench:
// build a 100-vertex / 500-edge graph and measure ListOutgoing on a
// hot vertex via the AGE Cypher path vs the legacy SQL path. Goal:
// AGE >= 2x faster than SQL.
//
// The graph is a "hub-and-spoke" — one root vertex with 99 OWNS
// edges to leaves, plus 401 random edges among the leaves to bring
// total edges to 500. ListOutgoing on the root touches 99 edges,
// which is where AGE's index-friendly graph storage outperforms
// jsonb-row scanning.
func BenchmarkListOutgoing_AGE_vs_SQL(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping testcontainers benchmark in -short mode")
	}

	store := sharedBenchStore(b)
	ctx := context.Background()

	// Reset the store and build the fixture once (outside the
	// timed loop). All sub-benchmarks share this graph.
	if err := store.truncateAll(ctx); err != nil {
		b.Fatalf("truncateAll: %v", err)
	}

	root := graph.Resource{Kind: "Deployment", Namespace: "perf", Name: "root"}
	if err := store.UpsertResource(ctx, root); err != nil {
		b.Fatalf("UpsertResource root: %v", err)
	}
	leaves := make([]graph.Resource, 99)
	for i := range leaves {
		leaves[i] = graph.Resource{
			Kind:      "ConfigMap",
			Namespace: "perf",
			Name:      fmt.Sprintf("cm-%02d", i),
		}
		if err := store.UpsertResource(ctx, leaves[i]); err != nil {
			b.Fatalf("UpsertResource leaf %d: %v", i, err)
		}
		// 99 edges from root to leaves.
		if err := store.UpsertEdge(ctx, graph.Edge{
			From: root.ID(), To: leaves[i].ID(), Type: graph.EdgeTypeOwns,
		}); err != nil {
			b.Fatalf("UpsertEdge root->leaf %d: %v", i, err)
		}
	}
	// Pad to 500 edges via a deterministic chain among leaves.
	for i := 0; i < 401; i++ {
		from := leaves[i%99]
		to := leaves[(i+1)%99]
		if err := store.UpsertEdge(ctx, graph.Edge{
			From: from.ID(), To: to.ID(), Type: graph.EdgeType("USES_CONFIGMAP"),
		}); err != nil {
			b.Fatalf("UpsertEdge filler %d: %v", i, err)
		}
	}

	rootID := root.ID()

	b.Run("AGE", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out, err := store.listOutgoingFromAGE(ctx, rootID)
			if err != nil {
				b.Fatalf("AGE: %v", err)
			}
			if len(out) != 99 {
				b.Fatalf("AGE: got %d edges, want 99", len(out))
			}
		}
	})
	// SQL path is what ListOutgoing actually uses in production —
	// see ListOutgoing in store.go for why we did not switch it to
	// AGE. The AGE sub-bench above stays as the perf-comparison
	// baseline so a future workload that flips the equation is
	// caught on regression.
	b.Run("SQL", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out, err := store.ListOutgoing(ctx, rootID)
			if err != nil {
				b.Fatalf("SQL: %v", err)
			}
			if len(out) != 99 {
				b.Fatalf("SQL: got %d edges, want 99", len(out))
			}
		}
	})
}

// BenchmarkUpsert1000Resources is the P2-T2 acceptance benchmark
// extended into P2-T4 to cover double-write (PG row + AGE vertex):
// each iteration upserts 1000 unique resources into a freshly-
// truncated store, so ns/op reports the end-to-end load latency.
//
// History:
//   - P2-T2 (PG-only): ~344us/upsert, ~344ms/iteration.
//   - P2-T4 (PG+AGE):  ~2.2ms/upsert, ~2.2s/iteration. The 6x cost
//     is the AGE MERGE round-trip plus the per-call LOAD/SET LOCAL
//     prelude in withAGETx; the alternative was AGE inconsistency,
//     which makes TraverseOutgoing's results stale.
//
// Wall-time budget: `make bench-postgres` runs -benchtime=1x to
// stay under 5s on a warm-cache CI runner; defaults run more
// iterations and exceed that budget.
func BenchmarkUpsert1000Resources(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping testcontainers benchmark in -short mode")
	}

	store := sharedBenchStore(b)
	ctx := context.Background()

	// Pre-build the resource fixtures so the timed loop measures the
	// upsert path itself, not Resource construction or fmt.Sprintf.
	resources := make([]graph.Resource, 1000)
	for i := range resources {
		resources[i] = graph.Resource{
			Kind:      "ConfigMap",
			Namespace: "bench",
			Name:      fmt.Sprintf("cm-%04d", i),
			Labels:    map[string]string{"app": "bench"},
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if err := store.truncateAll(ctx); err != nil {
			b.Fatalf("truncateAll: %v", err)
		}
		b.StartTimer()

		for _, r := range resources {
			if err := store.UpsertResource(ctx, r); err != nil {
				b.Fatalf("UpsertResource: %v", err)
			}
		}
	}
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"k8s.io/apimachinery/pkg/types"
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

// seedClusterFixture builds a fixture sized to match Phase 2's
// stress-5k-resources.sh shape (~7K resources, ~7-8K edges), but
// inside the postgres store so cluster + namespace aggregator
// benchmarks measure the actual store.Snapshot() round-trip cost.
//
// Per namespace: 1 Deployment, 1 ReplicaSet, 10 Pods (owner chain to
// the Deployment via the RS), 1 Service, 17 ConfigMaps referenced by
// the Deployment = 30 resources. Edges: pod→rs (10), rs→dep (1),
// dep→cm (17), svc→pod (10) = 38 in-ns edges. Plus one cross-ns
// edge per namespace (svc → next-ns Deployment) so the cluster
// aggregator has non-trivial cross-ns edge counts to fold.
//
// Truncates the store before seeding so the bench is deterministic
// regardless of which test ran before it.
func seedClusterFixture(b *testing.B, store *Store, namespaces int) {
	b.Helper()
	ctx := context.Background()
	if err := store.truncateAll(ctx); err != nil {
		b.Fatalf("seedClusterFixture: truncate: %v", err)
	}
	for i := 0; i < namespaces; i++ {
		ns := fmt.Sprintf("ns-%03d", i)
		depUID := types.UID(ns + "-dep")
		rsUID := types.UID(ns + "-rs")
		dep := graph.Resource{Kind: "Deployment", Namespace: ns, Name: "api", UID: depUID,
			Labels: map[string]string{"app": "api"}}
		rs := graph.Resource{Kind: "ReplicaSet", Namespace: ns, Name: "api-rs", UID: rsUID,
			OwnerReferences: []graph.OwnerRef{{Kind: "Deployment", Name: "api", UID: depUID}}}
		svc := graph.Resource{Kind: "Service", Namespace: ns, Name: "api-svc"}
		// Seed Deployment, RS, Service first so OwnerReferences lookup
		// during Pod upsert resolves the UID chain.
		for _, r := range []graph.Resource{dep, rs, svc} {
			if err := store.UpsertResource(ctx, r); err != nil {
				b.Fatalf("seedClusterFixture: upsert %s: %v", r.ID(), err)
			}
		}
		pods := make([]graph.Resource, 10)
		for j := range pods {
			pods[j] = graph.Resource{
				Kind: "Pod", Namespace: ns, Name: fmt.Sprintf("api-%d", j),
				UID:             types.UID(fmt.Sprintf("%s-p%d", ns, j)),
				OwnerReferences: []graph.OwnerRef{{Kind: "ReplicaSet", Name: "api-rs", UID: rsUID}},
			}
			if err := store.UpsertResource(ctx, pods[j]); err != nil {
				b.Fatalf("seedClusterFixture: upsert pod %d: %v", j, err)
			}
		}
		cms := make([]graph.Resource, 17)
		for j := range cms {
			cms[j] = graph.Resource{
				Kind: "ConfigMap", Namespace: ns, Name: fmt.Sprintf("cm-%02d", j),
			}
			if err := store.UpsertResource(ctx, cms[j]); err != nil {
				b.Fatalf("seedClusterFixture: upsert cm %d: %v", j, err)
			}
		}
		// Owner-chain edges + Deployment→ConfigMap envFrom edges +
		// Service→Pod selector edges. Mirrors the extractor output
		// shape in pkg/extractor for these kinds.
		for _, e := range edgesForNamespace(ns, dep, rs, svc, pods, cms) {
			if err := store.UpsertEdge(ctx, e); err != nil {
				b.Fatalf("seedClusterFixture: upsert edge %s→%s: %v", e.From, e.To, e.Type)
			}
		}
	}
	// Cross-namespace edges: ns[i].svc → ns[i+1].dep, so the cluster
	// aggregator has non-trivial cross-ns edges to fold.
	for i := 0; i < namespaces-1; i++ {
		from := fmt.Sprintf("ns-%03d/Service/api-svc", i)
		to := fmt.Sprintf("ns-%03d/Deployment/api", i+1)
		if err := store.UpsertEdge(ctx, graph.Edge{
			From: from, To: to, Type: graph.EdgeType("ROUTES_TO"),
		}); err != nil {
			b.Fatalf("seedClusterFixture: cross-ns edge %d: %v", i, err)
		}
	}
}

func edgesForNamespace(ns string, dep, rs, svc graph.Resource, pods, cms []graph.Resource) []graph.Edge {
	edges := make([]graph.Edge, 0, len(pods)+1+len(cms)+len(pods))
	for _, p := range pods {
		edges = append(edges, graph.Edge{From: p.ID(), To: rs.ID(), Type: graph.EdgeTypeOwns})
	}
	edges = append(edges, graph.Edge{From: rs.ID(), To: dep.ID(), Type: graph.EdgeTypeOwns})
	for _, cm := range cms {
		edges = append(edges, graph.Edge{From: dep.ID(), To: cm.ID(), Type: graph.EdgeTypeUsesConfigMap})
	}
	for _, p := range pods {
		edges = append(edges, graph.Edge{From: svc.ID(), To: p.ID(), Type: graph.EdgeTypeSelects})
	}
	_ = ns // reserved for future per-ns variation; keeps signature future-proof
	return edges
}

// BenchmarkP3T0aBaseline_Postgres captures the v1.0 baseline cost of
// cluster + namespace aggregation against a postgres-backed store on
// THIS hardware, before P3-T0a-v2 pushdown work. Pair this with the
// after-bench in the same file to get a hard before/after delta —
// the perf-baseline-v1.0.json numbers were captured on a different
// run with port-forward + WSL2 networking overhead included.
//
// Fixture size: 200 namespaces × 30 resources = 6000 resources,
// + 200 cross-ns edges + ~7600 in-ns edges. Comparable to the
// stress-5k-resources.sh fixture used in Phase 2's bench-v1.sh.
//
// Seed cost (~13s at 200 namespaces × 30 × ~2.2ms per UpsertResource)
// is amortised across both sub-benchmarks so `go test -bench` does
// not pay for it twice.
func BenchmarkP3T0aBaseline_Postgres(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping testcontainers benchmark in -short mode")
	}
	store := sharedBenchStore(b)
	const namespaces = 200
	seedClusterFixture(b, store, namespaces)
	ctx := context.Background()

	b.Run("ClusterAggregator", func(b *testing.B) {
		agg := aggregator.ClusterAggregator{}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			view, err := agg.Aggregate(ctx, store, aggregator.Scope{})
			if err != nil {
				b.Fatalf("Aggregate: %v", err)
			}
			if len(view.Nodes) != namespaces {
				b.Fatalf("cluster view: got %d nodes, want %d", len(view.Nodes), namespaces)
			}
		}
	})

	b.Run("NamespaceAggregator", func(b *testing.B) {
		agg := aggregator.NamespaceAggregator{}
		scope := aggregator.Scope{Namespace: "ns-100"}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := agg.Aggregate(ctx, store, scope); err != nil {
				b.Fatalf("Aggregate: %v", err)
			}
		}
	})
}

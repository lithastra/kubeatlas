package aggregator_test

import (
	"context"
	"strconv"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// seedSyntheticCluster builds a memory store shaped like the
// 1000-resource perf scenario in test/perf/stress-1k-configmaps.sh,
// without needing a real kind cluster. Every aggregator level can
// then be benchmarked with `go test -bench`.
//
// Layout:
//   - numNamespaces namespaces (default: 5)
//   - per namespace: 1 Deployment, 1 ReplicaSet (owned by Deployment),
//     2 Pods (owned by ReplicaSet), 1 Service selecting the Pods,
//     1 ConfigMap referenced by the Deployment
//   - one cross-namespace Ingress -> Service edge per namespace
//
// At the default 5 namespaces this is 30 resources + 30 edges. Bench
// callers scale via the namespaces parameter — 200 namespaces gets
// the store to ~1000 resources.
func seedSyntheticCluster(b *testing.B, namespaces int) graph.GraphStore {
	b.Helper()
	s := memory.New()
	ctx := context.Background()
	for i := 0; i < namespaces; i++ {
		ns := "ns-" + strconv.Itoa(i)
		dep := graph.Resource{Kind: "Deployment", Namespace: ns, Name: "api",
			UID: types.UID(ns + "-dep"), Labels: map[string]string{"app": "api"}}
		rs := graph.Resource{Kind: "ReplicaSet", Namespace: ns, Name: "api-rs",
			UID:             types.UID(ns + "-rs"),
			OwnerReferences: []graph.OwnerRef{{Kind: "Deployment", Name: "api", UID: types.UID(ns + "-dep")}}}
		pod1 := graph.Resource{Kind: "Pod", Namespace: ns, Name: "api-1",
			UID:             types.UID(ns + "-p1"),
			OwnerReferences: []graph.OwnerRef{{Kind: "ReplicaSet", Name: "api-rs", UID: types.UID(ns + "-rs")}}}
		pod2 := graph.Resource{Kind: "Pod", Namespace: ns, Name: "api-2",
			UID:             types.UID(ns + "-p2"),
			OwnerReferences: []graph.OwnerRef{{Kind: "ReplicaSet", Name: "api-rs", UID: types.UID(ns + "-rs")}}}
		svc := graph.Resource{Kind: "Service", Namespace: ns, Name: "api-svc"}
		cm := graph.Resource{Kind: "ConfigMap", Namespace: ns, Name: "app-config"}
		for _, r := range []graph.Resource{dep, rs, pod1, pod2, svc, cm} {
			_ = s.UpsertResource(ctx, r)
		}
		for _, e := range []graph.Edge{
			{From: pod1.ID(), To: rs.ID(), Type: graph.EdgeTypeOwns},
			{From: pod2.ID(), To: rs.ID(), Type: graph.EdgeTypeOwns},
			{From: rs.ID(), To: dep.ID(), Type: graph.EdgeTypeOwns},
			{From: dep.ID(), To: cm.ID(), Type: graph.EdgeTypeUsesConfigMap},
			{From: svc.ID(), To: pod1.ID(), Type: graph.EdgeTypeSelects},
			{From: svc.ID(), To: pod2.ID(), Type: graph.EdgeTypeSelects},
		} {
			_ = s.UpsertEdge(ctx, e)
		}
	}
	// Cross-namespace edges so cluster-level aggregation has work to do.
	for i := 0; i < namespaces-1; i++ {
		from := "ns-" + strconv.Itoa(i) + "/Service/api-svc"
		to := "ns-" + strconv.Itoa(i+1) + "/Service/api-svc"
		_ = s.UpsertEdge(ctx, graph.Edge{From: from, To: to, Type: graph.EdgeTypeRoutesTo})
	}
	return s
}

func BenchmarkClusterAggregator_5Namespaces(b *testing.B) {
	benchmarkCluster(b, 5)
}

func BenchmarkClusterAggregator_50Namespaces(b *testing.B) {
	benchmarkCluster(b, 50)
}

func BenchmarkClusterAggregator_200Namespaces(b *testing.B) {
	benchmarkCluster(b, 200) // ~1200 resources
}

func benchmarkCluster(b *testing.B, namespaces int) {
	store := seedSyntheticCluster(b, namespaces)
	agg := aggregator.ClusterAggregator{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := agg.Aggregate(context.Background(), store, aggregator.Scope{})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNamespaceAggregator(b *testing.B) {
	store := seedSyntheticCluster(b, 200)
	agg := aggregator.NamespaceAggregator{}
	scope := aggregator.Scope{Namespace: "ns-100"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := agg.Aggregate(context.Background(), store, scope)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWorkloadAggregator(b *testing.B) {
	store := seedSyntheticCluster(b, 200)
	agg := aggregator.WorkloadAggregator{}
	scope := aggregator.Scope{Namespace: "ns-100", Kind: "Deployment", Name: "api"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := agg.Aggregate(context.Background(), store, scope)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkResourceAggregator(b *testing.B) {
	store := seedSyntheticCluster(b, 200)
	agg := aggregator.ResourceAggregator{}
	scope := aggregator.Scope{Namespace: "ns-100", Kind: "Deployment", Name: "api"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := agg.Aggregate(context.Background(), store, scope)
		if err != nil {
			b.Fatal(err)
		}
	}
}

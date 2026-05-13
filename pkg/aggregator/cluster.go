package aggregator

import (
	"context"
	"sort"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// ClusterAggregator collapses the entire graph into one node per
// namespace plus one aggregated edge per (from-ns -> to-ns) pair.
// Cluster-scoped resources (Namespace itself and resources without
// a namespace) are bundled into the "_cluster" pseudo-namespace.
type ClusterAggregator struct{}

func (ClusterAggregator) Level() Level { return LevelCluster }

// clusterScopedBucket is the namespace label the cluster view uses
// for resources without a Namespace (Resource.Namespace == ""). The
// GraphStore pushdown methods return the empty string for these
// resources; the aggregator relabels to "_cluster" so the View
// presents a stable, non-empty node ID to the UI.
//
// Anti-pattern guarded: do not do this relabel inside the GraphStore
// pushdown methods. The store returns raw data; presentation is the
// aggregator's job. Pushing this into KindCountsByNamespace would
// hide cluster-scoped vs ns="_cluster" (an unusable but legal name)
// from the store layer.
const clusterScopedBucket = "_cluster"

func (ClusterAggregator) Aggregate(ctx context.Context, store graph.GraphStore, _ Scope) (*View, error) {
	// Pushdown path (P3-T0a, May 2026): the old implementation called
	// store.Snapshot, which materialised every Resource (including
	// the full Raw K8s payload) into Go heap on every request — 50-
	// 200 MB per call on a 6-7K resource cluster and an OOM-kill of
	// the API pod under modest concurrent load. The new path uses
	// two GROUP BY queries that return ~100 + ~50 small rows.
	nsKinds, err := store.KindCountsByNamespace(ctx)
	if err != nil {
		return nil, err
	}
	edgeCounts, err := store.CrossNamespaceEdgeCounts(ctx)
	if err != nil {
		return nil, err
	}

	// Relabel the cluster-scoped bucket. KindCountsByNamespace returns
	// resources with empty-string Namespace under the "" key; the
	// View exposes them as "_cluster".
	if cs, ok := nsKinds[""]; ok {
		nsKinds[clusterScopedBucket] = cs
		delete(nsKinds, "")
	}

	// Per-namespace edge in/out totals (cross-ns only — same-ns edges
	// fold inside the namespace node and aren't visible at cluster
	// level).
	in := make(map[string]int)
	out := make(map[string]int)
	for k, c := range edgeCounts {
		if k.From == k.To {
			continue
		}
		from := bucketize(k.From)
		to := bucketize(k.To)
		out[from] += c
		in[to] += c
	}

	// Initialise to non-nil empty slices so JSON encoding emits []
	// instead of null when there's no aggregated content. The Web UI
	// iterates these directly, so null would throw a TypeError on the
	// client.
	view := &View{Level: LevelCluster, Nodes: []Node{}, Edges: []AEdge{}}
	for _, ns := range sortedKeys(nsKinds) {
		view.Nodes = append(view.Nodes, Node{
			ID:              ns,
			Type:            "aggregated",
			Level:           LevelNamespace,
			Label:           ns,
			ChildrenCount:   sumValues(nsKinds[ns]),
			ChildrenSummary: foldKindSummary(nsKinds[ns], summaryKindLimit),
			EdgeCountIn:     in[ns],
			EdgeCountOut:    out[ns],
		})
	}

	// Emit aggregated edges only for cross-namespace pairs, sorted
	// (from, to) for deterministic output.
	type pair struct{ from, to string }
	keys := make([]pair, 0, len(edgeCounts))
	bucketedCounts := make(map[pair]int, len(edgeCounts))
	for k, c := range edgeCounts {
		from := bucketize(k.From)
		to := bucketize(k.To)
		if from == to {
			continue
		}
		p := pair{from, to}
		if _, seen := bucketedCounts[p]; !seen {
			keys = append(keys, p)
		}
		bucketedCounts[p] += c
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].from != keys[j].from {
			return keys[i].from < keys[j].from
		}
		return keys[i].to < keys[j].to
	})
	for _, k := range keys {
		view.Edges = append(view.Edges, AEdge{
			From:  k.from,
			To:    k.to,
			Count: bucketedCounts[k],
		})
	}

	return view, nil
}

// bucketize applies the cluster-scoped relabel rule to a raw
// namespace string from the store. Used for edge endpoints —
// resource counts are relabeled in bulk on the nsKinds map.
func bucketize(ns string) string {
	if ns == "" {
		return clusterScopedBucket
	}
	return ns
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sumValues(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}

// foldKindSummary returns a kind-frequency map where the top topN
// kinds (by count, ties broken alphabetically) are kept verbatim and
// the rest are folded into "Other".
func foldKindSummary(counts map[string]int, topN int) map[string]int {
	if len(counts) == 0 {
		return nil
	}
	type kc struct {
		kind  string
		count int
	}
	kcs := make([]kc, 0, len(counts))
	for k, c := range counts {
		kcs = append(kcs, kc{k, c})
	}
	sort.Slice(kcs, func(i, j int) bool {
		if kcs[i].count != kcs[j].count {
			return kcs[i].count > kcs[j].count
		}
		return kcs[i].kind < kcs[j].kind
	})

	out := make(map[string]int, topN+1)
	other := 0
	for i, kc := range kcs {
		if i < topN {
			out[kc.kind] = kc.count
		} else {
			other += kc.count
		}
	}
	if other > 0 {
		out["Other"] = other
	}
	return out
}

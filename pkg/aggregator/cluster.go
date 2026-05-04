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

func (ClusterAggregator) Aggregate(ctx context.Context, store graph.GraphStore, _ Scope) (*View, error) {
	snap, err := store.Snapshot(ctx)
	if err != nil {
		return nil, err
	}

	// Pass 1: bucket resources by namespace, count Kind frequencies.
	nsKinds := make(map[string]map[string]int) // ns -> kind -> count
	for _, r := range snap.Resources {
		ns := nsKey(r)
		if nsKinds[ns] == nil {
			nsKinds[ns] = make(map[string]int)
		}
		nsKinds[ns][r.Kind]++
	}

	// Build a fast lookup ns(ID) so we don't re-parse ids during pass 2.
	nsOf := make(map[string]string, len(snap.Resources))
	for _, r := range snap.Resources {
		nsOf[r.ID()] = nsKey(r)
	}

	// Pass 2: fold edges into (from-ns, to-ns) pairs.
	type edgeKey struct{ from, to string }
	edgeCounts := make(map[edgeKey]int)
	for _, e := range snap.Edges {
		from := nsOf[e.From]
		to := nsOf[e.To]
		if from == "" || to == "" {
			continue
		}
		edgeCounts[edgeKey{from, to}]++
	}

	// Per-namespace edge in/out totals (counting cross-ns only? the
	// spec example shows 0/0 for petclinic which suggests in/out count
	// only edges crossing namespace boundaries — same-ns edges fold
	// inside the namespace node and aren't visible at cluster level).
	in := make(map[string]int)
	out := make(map[string]int)
	for k, c := range edgeCounts {
		if k.from == k.to {
			continue
		}
		out[k.from] += c
		in[k.to] += c
	}

	view := &View{Level: LevelCluster}
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
	keys := make([]edgeKey, 0, len(edgeCounts))
	for k := range edgeCounts {
		if k.from == k.to {
			continue
		}
		keys = append(keys, k)
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
			Count: edgeCounts[k],
		})
	}

	return view, nil
}

// nsKey returns the bucket name for a resource: its Namespace, or
// "_cluster" for cluster-scoped resources.
func nsKey(r graph.Resource) string {
	if r.Namespace == "" {
		return "_cluster"
	}
	return r.Namespace
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

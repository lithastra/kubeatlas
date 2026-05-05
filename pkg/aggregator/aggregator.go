package aggregator

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Level enumerates the aggregation views the API exposes.
//
// All four levels are live as of Phase 1 W5. Cluster and namespace
// levels emit aggregated nodes (Type="aggregated"); workload and
// resource levels emit raw resource nodes (Type="resource") because
// at those zoom levels the user wants to see individual K8s objects,
// not summaries.
type Level string

const (
	LevelCluster   Level = "cluster"
	LevelNamespace Level = "namespace"
	LevelWorkload  Level = "workload"
	LevelResource  Level = "resource"
)

// Scope narrows down what an Aggregator returns.
//
//	LevelCluster:   all fields ignored.
//	LevelNamespace: Namespace required.
//	LevelWorkload:  Namespace + Kind + Name required (must point at a workload).
//	LevelResource:  Namespace + Kind + Name required (must point at any resource).
type Scope struct {
	Namespace string
	Kind      string
	Name      string
}

// Aggregator computes a pre-aggregated view of the graph at a given
// level. Implementations re-compute on every call — Phase 0 has no
// caching, by design. LRU / incremental aggregation is deferred to
// Phase 1 W5+ (see the project guides).
type Aggregator interface {
	Level() Level
	Aggregate(ctx context.Context, store graph.GraphStore, scope Scope) (*View, error)
}

// View is the pre-aggregated output served to clients (and to the CLI
// across all four levels). Its JSON shape is the public contract the
// Web UI consumes — fields are append-only.
type View struct {
	Level Level   `json:"level"`
	Nodes []Node  `json:"nodes"`
	Edges []AEdge `json:"edges"`
	// Truncated is true when an aggregator dropped neighbors to stay
	// under a per-level cap (resource-level renderers prefer ≤30
	// neighbors; see MaxResourceNeighbors). Always false at cluster /
	// namespace / workload levels today.
	Truncated bool `json:"truncated,omitempty"`
	// Mermaid is a server-generated flowchart text the client can hand
	// straight to mermaid.render. Only populated by ResourceAggregator
	// (and only when nodes <= MaxResourceNeighbors so Mermaid can
	// render it without thrashing). Centralising the text generation
	// here keeps escape rules + node-id mapping in one place; clients
	// don't have to re-implement it for the CLI vs. UI vs. plugin
	// surfaces. Empty for other levels.
	Mermaid string `json:"mermaid,omitempty"`
}

// Node represents either a raw resource (Type="resource") or an
// aggregated wrapper around a set of resources (Type="aggregated").
//
// For aggregated nodes:
//   - ID is the cluster-unique key (a namespace name, a workload name)
//   - Label is what the UI displays
//   - ChildrenCount counts the underlying resources
//   - ChildrenSummary maps Kind to count, with kinds beyond the
//     top-N folded into "Other"
//
// For raw nodes (used in namespace-level views for resources that
// aren't aggregated):
//   - ID matches graph.Resource.ID()
//   - Kind / Namespace / Name carry the underlying resource identity
type Node struct {
	ID              string         `json:"id"`
	Type            string         `json:"type"` // "resource" or "aggregated"
	Level           Level          `json:"level,omitempty"`
	Label           string         `json:"label,omitempty"`
	Kind            string         `json:"kind,omitempty"`
	Namespace       string         `json:"namespace,omitempty"`
	Name            string         `json:"name,omitempty"`
	ChildrenCount   int            `json:"children_count,omitempty"`
	ChildrenSummary map[string]int `json:"children_summary,omitempty"`
	EdgeCountIn     int            `json:"edge_count_in"`
	EdgeCountOut    int            `json:"edge_count_out"`
}

// AEdge is an edge in the aggregated view. For raw edges Count is 1;
// for aggregated edges (cluster-level: A-namespace -> B-namespace),
// Count tallies how many underlying graph.Edges were folded together.
type AEdge struct {
	From  string         `json:"from"`
	To    string         `json:"to"`
	Type  graph.EdgeType `json:"type,omitempty"`
	Count int            `json:"count"`
}

// summaryKindLimit caps how many distinct Kinds appear in a
// ChildrenSummary map; the rest fold into "Other". Picked so PetClinic
// summaries fit on one screen without scrolling.
const summaryKindLimit = 5

// resourceNode wraps a graph.Resource as a raw (Type="resource") view
// node. Used by the workload and resource levels.
func resourceNode(r graph.Resource) Node {
	return Node{
		ID:        r.ID(),
		Type:      "resource",
		Kind:      r.Kind,
		Namespace: r.Namespace,
		Name:      r.Name,
	}
}

// brokenNode emits a placeholder for a target id whose resource isn't
// in the store (dangling edge). The UI can render these distinctly.
func brokenNode(id string) Node {
	return Node{
		ID:    id,
		Type:  "resource",
		Label: "(missing) " + id,
	}
}

// sortEdges returns edges sorted by (from, to, type) so callers don't
// have to think about determinism.
func sortEdges(in []graph.Edge) []AEdge {
	out := make([]AEdge, 0, len(in))
	for _, e := range in {
		out = append(out, AEdge{From: e.From, To: e.To, Type: e.Type, Count: 1})
	}
	// Stable sort so duplicate (from,to,type) entries — which can happen
	// when the workload aggregator pulls the same edge in via two
	// different walks — preserve their original relative order.
	deduped := make([]AEdge, 0, len(out))
	seen := make(map[struct {
		from, to string
		t        graph.EdgeType
	}]bool, len(out))
	for _, e := range out {
		k := struct {
			from, to string
			t        graph.EdgeType
		}{e.From, e.To, e.Type}
		if seen[k] {
			continue
		}
		seen[k] = true
		deduped = append(deduped, e)
	}
	// Sort for deterministic output.
	sortAEdges(deduped)
	return deduped
}

func sortAEdges(es []AEdge) {
	// Insertion sort — fine for the sizes a single view returns (≤ a
	// few hundred edges), and avoids pulling in sort.Slice closures.
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && lessAEdge(es[j], es[j-1]); j-- {
			es[j], es[j-1] = es[j-1], es[j]
		}
	}
}

func lessAEdge(a, b AEdge) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	if a.To != b.To {
		return a.To < b.To
	}
	return a.Type < b.Type
}

// annotateEdgeCounts fills in Node.EdgeCountIn / EdgeCountOut from the
// view's edges. Workload + resource levels use this; cluster + namespace
// levels compute their own counts inline.
func annotateEdgeCounts(view *View) {
	idx := make(map[string]int, len(view.Nodes))
	for i, n := range view.Nodes {
		idx[n.ID] = i
	}
	for _, e := range view.Edges {
		if i, ok := idx[e.From]; ok {
			view.Nodes[i].EdgeCountOut += e.Count
		}
		if i, ok := idx[e.To]; ok {
			view.Nodes[i].EdgeCountIn += e.Count
		}
	}
}

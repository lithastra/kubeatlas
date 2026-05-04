package aggregator

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Level enumerates the aggregation views the API exposes.
//
// Phase 0 implements cluster + namespace levels; the workload and
// resource levels (commented out below) land in Phase 1 W5.
type Level string

const (
	LevelCluster   Level = "cluster"
	LevelNamespace Level = "namespace"
	// LevelWorkload Level = "workload" — Phase 1 W5
	// LevelResource Level = "resource" — Phase 1 W5
)

// Scope narrows down what an Aggregator returns. For LevelCluster all
// fields are ignored; for LevelNamespace, Namespace is required.
type Scope struct {
	Namespace string
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
// in -level=cluster / -level=namespace modes). Its JSON shape is the
// public contract the Web UI consumes — fields are append-only.
type View struct {
	Level Level   `json:"level"`
	Nodes []Node  `json:"nodes"`
	Edges []AEdge `json:"edges"`
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

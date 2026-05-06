package aggregator

import (
	"context"
	"errors"
	"sort"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// MaxResourceNeighbors caps the number of one-hop neighbors a resource-
// level view returns. The Web UI renders this view with Mermaid (added
// in Phase 1 W9), which copes poorly above ~50 nodes; 30 leaves head-
// room and matches the Phase 1 spec.
const MaxResourceNeighbors = 30

// ResourceAggregator returns the sub-graph rooted at a single resource
// plus one hop of neighbors in either direction (incoming + outgoing
// edges). Result is capped at MaxResourceNeighbors + 1 nodes; when the
// cap trips the View.Truncated flag is set so the UI can warn the user
// and link them to the workload-level view instead.
type ResourceAggregator struct{}

func (ResourceAggregator) Level() Level { return LevelResource }

func (ResourceAggregator) Aggregate(ctx context.Context, store graph.GraphStore, scope Scope) (*View, error) {
	if scope.Namespace == "" || scope.Kind == "" || scope.Name == "" {
		return nil, errors.New("resource level requires Scope.Namespace + Kind + Name")
	}
	rootID := scope.Namespace + "/" + scope.Kind + "/" + scope.Name
	root, err := store.GetResource(ctx, rootID)
	if err != nil {
		return nil, err
	}

	incoming, err := store.ListIncoming(ctx, rootID)
	if err != nil {
		return nil, err
	}
	outgoing, err := store.ListOutgoing(ctx, rootID)
	if err != nil {
		return nil, err
	}

	// Determine the neighbor id set, deterministic order so truncation
	// is reproducible.
	neighborSet := map[string]bool{}
	for _, e := range incoming {
		neighborSet[e.From] = true
	}
	for _, e := range outgoing {
		neighborSet[e.To] = true
	}
	neighborIDs := make([]string, 0, len(neighborSet))
	for id := range neighborSet {
		neighborIDs = append(neighborIDs, id)
	}
	sort.Strings(neighborIDs)

	view := &View{Level: LevelResource, Nodes: []Node{}, Edges: []AEdge{}}
	view.Nodes = append(view.Nodes, resourceNode(root))

	truncated := false
	if len(neighborIDs) > MaxResourceNeighbors {
		neighborIDs = neighborIDs[:MaxResourceNeighbors]
		truncated = true
	}
	keep := map[string]bool{rootID: true}
	for _, id := range neighborIDs {
		keep[id] = true
		r, err := store.GetResource(ctx, id)
		if err != nil {
			view.Nodes = append(view.Nodes, brokenNode(id))
			continue
		}
		view.Nodes = append(view.Nodes, resourceNode(r))
	}

	// Emit only edges whose endpoints both made it into the view.
	var edges []graph.Edge
	for _, e := range incoming {
		if keep[e.From] {
			edges = append(edges, e)
		}
	}
	for _, e := range outgoing {
		if keep[e.To] {
			edges = append(edges, e)
		}
	}
	view.Edges = sortEdges(edges)
	view.Truncated = truncated
	annotateEdgeCounts(view)
	view.Mermaid = renderMermaid(view)
	return view, nil
}

package aggregator

import (
	"context"
	"errors"
	"sort"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// WorkloadAggregator returns the sub-graph rooted at a single workload
// (Deployment / StatefulSet / DaemonSet / Job / CronJob / Service /
// Ingress / HTTPRoute). The returned view contains:
//
//   - the workload itself
//   - every transitively-owned descendant reachable via OWNS edges
//     (ReplicaSet, Pod) — in K8s the OWNS edge points owned→owner,
//     so descending the chain means walking incoming OWNS edges
//   - every directly-referenced resource of any other edge type
//     (USES_CONFIGMAP, USES_SECRET, MOUNTS_VOLUME,
//     USES_SERVICEACCOUNT, ROUTES_TO, ATTACHED_TO, SELECTS),
//     pulled from the workload and any of its descendants
//
// Nodes carry Type="resource"; this view is not aggregated, it is
// scope-filtered. Edges carry Count=1.
type WorkloadAggregator struct{}

func (WorkloadAggregator) Level() Level { return LevelWorkload }

func (WorkloadAggregator) Aggregate(ctx context.Context, store graph.GraphStore, scope Scope) (*View, error) {
	if scope.Namespace == "" || scope.Kind == "" || scope.Name == "" {
		return nil, errors.New("workload level requires Scope.Namespace + Kind + Name")
	}
	rootID := scope.Namespace + "/" + scope.Kind + "/" + scope.Name
	if _, err := store.GetResource(ctx, rootID); err != nil {
		return nil, err
	}

	// Phase 1: walk the OWNS chain downwards (BFS over incoming OWNS).
	owned := map[string]bool{rootID: true}
	queue := []string{rootID}
	var ownsEdges []graph.Edge
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		incoming, err := store.ListIncoming(ctx, cur)
		if err != nil {
			return nil, err
		}
		for _, e := range incoming {
			if e.Type != graph.EdgeTypeOwns {
				continue
			}
			ownsEdges = append(ownsEdges, e)
			if !owned[e.From] {
				owned[e.From] = true
				queue = append(queue, e.From)
			}
		}
	}

	// Phase 2: for every node in the OWNS sub-tree, pull in its directly-
	// referenced resources via outgoing non-OWNS edges (config, secret,
	// volume, sa, routes, attached, selects).
	included := map[string]bool{}
	for id := range owned {
		included[id] = true
	}
	var refEdges []graph.Edge
	for id := range owned {
		out, err := store.ListOutgoing(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, e := range out {
			if e.Type == graph.EdgeTypeOwns {
				continue // OWNS goes upward (owned -> owner) — already covered
			}
			refEdges = append(refEdges, e)
			included[e.To] = true
		}
	}

	// Materialise nodes from the included id set.
	view := &View{Level: LevelWorkload, Nodes: []Node{}, Edges: []AEdge{}}
	ids := make([]string, 0, len(included))
	for id := range included {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r, err := store.GetResource(ctx, id)
		if err != nil {
			// A referenced target may not exist (dangling ref); emit a
			// best-effort node so the UI can render the broken edge.
			view.Nodes = append(view.Nodes, brokenNode(id))
			continue
		}
		view.Nodes = append(view.Nodes, resourceNode(r))
	}

	// Edges: OWNS sub-tree + non-OWNS references, deterministic order.
	all := append(ownsEdges, refEdges...)
	view.Edges = sortEdges(all)
	annotateEdgeCounts(view)
	return view, nil
}

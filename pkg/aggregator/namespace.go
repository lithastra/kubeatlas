package aggregator

import (
	"context"
	"errors"
	"sort"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// NamespaceAggregator collapses a single namespace into a workload-
// centric view: each workload kind (Deployment, StatefulSet, etc.) is
// an aggregated node whose ChildrenCount tallies its OwnerRef-reachable
// Pods and ReplicaSets. ConfigMap, Secret, ServiceAccount, and PVC
// stay as raw resource nodes — they're the "leaves" the workloads
// reference.
type NamespaceAggregator struct{}

func (NamespaceAggregator) Level() Level { return LevelNamespace }

// workloadKinds is the set of kinds promoted to aggregated nodes at
// namespace level. Other kinds either pass through as raw resource
// nodes (ConfigMap / Secret / SA / PVC) or are filtered out (Pod,
// ReplicaSet — they're absorbed into their owning workload).
var workloadKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"Job":         true,
	"CronJob":     true,
	"Service":     true,
	"Ingress":     true,
	"HTTPRoute":   true,
}

// raw passthroughKinds keep their resource node form.
var passthroughKinds = map[string]bool{
	"ConfigMap":             true,
	"Secret":                true,
	"ServiceAccount":        true,
	"PersistentVolumeClaim": true,
	"Gateway":               true,
}

// absorbedKinds are folded into their owners' ChildrenCount and not
// emitted as standalone nodes.
var absorbedKinds = map[string]bool{
	"Pod":        true,
	"ReplicaSet": true,
}

func (NamespaceAggregator) Aggregate(ctx context.Context, store graph.GraphStore, scope Scope) (*View, error) {
	if scope.Namespace == "" {
		return nil, errors.New("namespace level requires Scope.Namespace")
	}

	// Pushdown path (P3-T0a, May 2026): the old implementation called
	// store.Snapshot which materialised every Resource in the entire
	// store (typically 10× the size of the target namespace) just to
	// throw away everything outside scope.Namespace. NamespaceSubgraph
	// returns only the in-namespace resources + in-namespace edges in
	// one round-trip, cutting per-call heap allocation and the API
	// pod's OOM risk on multi-namespace clusters.
	//
	// K8s OwnerReferences point to same-namespace resources by spec
	// rule (cluster-scoped owners excepted), so the owner-chain walk
	// below runs correctly against the subgraph without ever fetching
	// the rest of the store.
	// scope.Labels (F-114) is pushed into the subgraph query: only
	// resources carrying every key=value pair, and edges between two
	// such resources, come back.
	sub, err := store.NamespaceSubgraph(ctx, scope.Namespace, scope.Labels)
	if err != nil {
		return nil, err
	}

	// Build a quick UID → resource index for owner-chain walking.
	// Every resource in the subgraph is in scope.Namespace already.
	byUID := make(map[string]graph.Resource, len(sub.Resources))
	byID := make(map[string]graph.Resource, len(sub.Resources))
	for _, r := range sub.Resources {
		if r.UID != "" {
			byUID[string(r.UID)] = r
		}
		byID[r.ID()] = r
	}

	// nsRes is every resource the aggregator's downstream loops
	// iterate over. Identical to sub.Resources now that the store
	// has done the namespace filter; kept as a named slice so the
	// rest of the function reads the same as before the refactor.
	nsRes := sub.Resources

	view := &View{Level: LevelNamespace, Nodes: []Node{}, Edges: []AEdge{}}

	// Emit aggregated nodes for workloads. ChildrenCount for each
	// workload counts the number of resources whose owner chain leads
	// back to it.
	workloads := make(map[string]graph.Resource) // workload ID -> resource
	for _, r := range nsRes {
		if workloadKinds[r.Kind] {
			workloads[r.ID()] = r
		}
	}
	childCounts := make(map[string]int)
	for _, r := range nsRes {
		if !absorbedKinds[r.Kind] {
			continue
		}
		root := walkToWorkload(r, byUID, workloads)
		if root != "" {
			childCounts[root]++
		}
	}

	for _, id := range sortedKeys(workloads) {
		r := workloads[id]
		view.Nodes = append(view.Nodes, Node{
			ID:            id,
			Type:          "aggregated",
			Level:         LevelNamespace,
			Label:         r.Kind + "/" + r.Name,
			Kind:          r.Kind,
			Namespace:     r.Namespace,
			Name:          r.Name,
			ChildrenCount: childCounts[id],
		})
	}

	// Emit raw nodes for passthrough kinds.
	rawIDs := make([]string, 0)
	for _, r := range nsRes {
		if passthroughKinds[r.Kind] {
			rawIDs = append(rawIDs, r.ID())
		}
	}
	sort.Strings(rawIDs)
	for _, id := range rawIDs {
		r := byID[id]
		view.Nodes = append(view.Nodes, Node{
			ID:        id,
			Type:      "resource",
			Kind:      r.Kind,
			Namespace: r.Namespace,
			Name:      r.Name,
		})
	}

	// Visible-node set: everything we're going to emit a node for.
	visible := make(map[string]bool, len(view.Nodes))
	for _, n := range view.Nodes {
		visible[n.ID] = true
	}

	// Walk edges. For each edge whose endpoints are in the namespace,
	// rewrite Pod/ReplicaSet endpoints to their owning workload, then
	// emit the edge if both endpoints are visible.
	edgeCounts := make(map[struct {
		from, to string
		t        graph.EdgeType
	}]int)
	for _, e := range sub.Edges {
		from := normalizeEndpoint(e.From, byID, byUID, workloads, visible)
		to := normalizeEndpoint(e.To, byID, byUID, workloads, visible)
		if from == "" || to == "" {
			continue
		}
		edgeCounts[struct {
			from, to string
			t        graph.EdgeType
		}{from, to, e.Type}]++
	}
	keys := make([]struct {
		from, to string
		t        graph.EdgeType
	}, 0, len(edgeCounts))
	for k := range edgeCounts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].from != keys[j].from {
			return keys[i].from < keys[j].from
		}
		if keys[i].to != keys[j].to {
			return keys[i].to < keys[j].to
		}
		return keys[i].t < keys[j].t
	})
	for _, k := range keys {
		view.Edges = append(view.Edges, AEdge{
			From:  k.from,
			To:    k.to,
			Type:  k.t,
			Count: edgeCounts[k],
		})
	}

	// Compute per-node edge in/out counts.
	nodeIdx := make(map[string]int, len(view.Nodes))
	for i, n := range view.Nodes {
		nodeIdx[n.ID] = i
	}
	for _, e := range view.Edges {
		if i, ok := nodeIdx[e.From]; ok {
			view.Nodes[i].EdgeCountOut += e.Count
		}
		if i, ok := nodeIdx[e.To]; ok {
			view.Nodes[i].EdgeCountIn += e.Count
		}
	}

	return view, nil
}

// walkToWorkload follows owner references from r upwards until it
// reaches a workload (one of the IDs in workloads). Returns "" if the
// chain ends without hitting a workload (orphan Pod, etc.).
func walkToWorkload(r graph.Resource, byUID map[string]graph.Resource, workloads map[string]graph.Resource) string {
	cur := r
	visited := make(map[string]bool)
	for {
		if _, ok := workloads[cur.ID()]; ok {
			return cur.ID()
		}
		if visited[cur.ID()] {
			return "" // owner-ref cycle, give up
		}
		visited[cur.ID()] = true
		if len(cur.OwnerReferences) == 0 {
			return ""
		}
		owner := cur.OwnerReferences[0]
		next, ok := byUID[string(owner.UID)]
		if !ok {
			return ""
		}
		cur = next
	}
}

// normalizeEndpoint rewrites Pod/ReplicaSet endpoints to their owning
// workload (so an edge from a Pod becomes an edge from its Deployment
// at namespace level), and returns "" for endpoints we don't render.
func normalizeEndpoint(id string, byID map[string]graph.Resource, byUID map[string]graph.Resource, workloads map[string]graph.Resource, visible map[string]bool) string {
	if visible[id] {
		return id
	}
	r, ok := byID[id]
	if !ok {
		return "" // edge points outside the snapshot
	}
	if absorbedKinds[r.Kind] {
		return walkToWorkload(r, byUID, workloads)
	}
	return ""
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// CycleReport names a strongly connected component of size > 1 in
// the graph — i.e. a set of resources that form a directed cycle.
// Members is sorted by ID so consumers can diff successive runs
// without re-sorting.
//
// In a healthy K8s graph this list is empty. A non-empty result
// is almost always a config error: ConfigMaps that reference each
// other through annotations, OwnerReferences that loop (informer
// bug or malicious mutation), Service selectors that pick up
// their own backing Pod's labels in pathological setups, etc.
type CycleReport struct {
	Members []graph.Resource `json:"members"`
}

// DetectCycles returns every strongly connected component with at
// least two members. Self-loops (single-vertex SCCs that have an
// edge to themselves) are intentionally not reported — they are
// vanishingly rare in K8s and the playbook explicitly carves them
// out so dashboards do not show noisy "ConfigMap X owns ConfigMap
// X" entries when an extractor mis-fires.
//
// Implementation is Tarjan's SCC algorithm — O(V + E) — which the
// playbook prescribes over a hand-rolled DFS + visited-set both
// for correctness (Tarjan is the textbook answer) and for
// performance: a 5K-vertex / 5K-edge graph runs comfortably under
// the 200ms playbook budget on commodity hardware.
func DetectCycles(ctx context.Context, store graph.GraphStore) ([]CycleReport, error) {
	snap, err := store.Snapshot(ctx)
	if err != nil {
		return nil, err
	}

	// Map every resource ID to a dense int index so the Tarjan
	// state arrays can be flat slices (cheaper than hashed maps
	// in the inner loop).
	indexByID := make(map[string]int, len(snap.Resources))
	resources := make([]graph.Resource, 0, len(snap.Resources))
	for _, r := range snap.Resources {
		indexByID[r.ID()] = len(resources)
		resources = append(resources, r)
	}

	// Build adjacency from the snapshot's edges. Edges that
	// reference a vertex we don't have (dangling target) are
	// silently dropped — Tarjan can only reason about the closed
	// universe of vertices it sees.
	adj := make([][]int, len(resources))
	for _, e := range snap.Edges {
		from, ok := indexByID[e.From]
		if !ok {
			continue
		}
		to, ok := indexByID[e.To]
		if !ok {
			continue
		}
		adj[from] = append(adj[from], to)
	}

	t := tarjan{
		adj:     adj,
		index:   make([]int, len(resources)),
		lowlink: make([]int, len(resources)),
		onStack: make([]bool, len(resources)),
	}
	for i := range t.index {
		t.index[i] = -1
	}

	for v := range adj {
		if t.index[v] == -1 {
			t.strongConnect(v)
		}
	}

	out := make([]CycleReport, 0, len(t.components))
	for _, comp := range t.components {
		if len(comp) < 2 {
			continue
		}
		members := make([]graph.Resource, 0, len(comp))
		for _, idx := range comp {
			members = append(members, resources[idx])
		}
		sortByID(members)
		out = append(out, CycleReport{Members: members})
	}
	return out, nil
}

// tarjan holds the bookkeeping state Tarjan's algorithm needs.
// Kept private so callers can't accidentally reuse a partially-
// drained struct.
type tarjan struct {
	adj        [][]int
	index      []int
	lowlink    []int
	onStack    []bool
	stack      []int
	counter    int
	components [][]int
}

func (t *tarjan) strongConnect(v int) {
	t.index[v] = t.counter
	t.lowlink[v] = t.counter
	t.counter++
	t.stack = append(t.stack, v)
	t.onStack[v] = true

	for _, w := range t.adj[v] {
		switch {
		case t.index[w] == -1:
			t.strongConnect(w)
			if t.lowlink[w] < t.lowlink[v] {
				t.lowlink[v] = t.lowlink[w]
			}
		case t.onStack[w]:
			// Back-edge to a vertex still on the stack — that
			// closes a cycle. Update lowlink with the target's
			// DFS index, not its lowlink (Tarjan's classic rule).
			if t.index[w] < t.lowlink[v] {
				t.lowlink[v] = t.index[w]
			}
		}
	}

	// If v is the root of an SCC, peel the stack down to v.
	if t.lowlink[v] == t.index[v] {
		var comp []int
		for {
			n := len(t.stack) - 1
			w := t.stack[n]
			t.stack = t.stack[:n]
			t.onStack[w] = false
			comp = append(comp, w)
			if w == v {
				break
			}
		}
		t.components = append(t.components, comp)
	}
}

// sortByID orders Members deterministically so successive
// snapshots of the same cycle look identical to consumers.
func sortByID(rs []graph.Resource) {
	// Insertion sort is fine — cycles are tiny in practice (rarely
	// more than a handful of members).
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j-1].ID() > rs[j].ID(); j-- {
			rs[j-1], rs[j] = rs[j], rs[j-1]
		}
	}
}

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
// In a healthy K8s graph the unknown-category list is empty. A
// non-empty unknown result is almost always a config error:
// ConfigMaps that reference each other through annotations,
// OwnerReferences that loop (informer bug or malicious mutation),
// Service selectors that pick up their own backing Pod's labels in
// pathological setups, etc.
//
// Category was added in P3-T0b (May 2026) so verifiers and the
// GitHub Action (F-202) can map cycle severity reliably without
// the regex-style filtering that phase2.sh used through Phase 2.
type CycleReport struct {
	Members  []graph.Resource `json:"members"`
	Category CycleCategory    `json:"category"`
}

// CycleCategory classifies a detected cycle by its real-world
// shape. The enum is intentionally small in v1.0.1 — additional
// categories (mtls-mesh, gitops-flux, ...) land in v1.2 once we
// have data on which patterns merit a dedicated bucket vs.
// staying in "unknown".
//
// Wire-format enum strings are stable across versions. The JSON
// shape is "<bucket>" (lowercase, hyphenated). Clients should
// treat unrecognised values as "unknown" rather than erroring.
type CycleCategory string

const (
	// CycleCategoryBootstrapCert tags the common "controller owns
	// its own webhook cert Secret AND consumes that Secret" pattern
	// that cert-manager, CNPG, kyverno, etc. all emit. Real cycle,
	// benign at the operational level — operators ship them this
	// way on purpose. Shape: 2-member SCC, one member is
	// Kind=Secret with an OwnerReferences entry naming the other
	// member. This shape match mirrors the JQ filter in
	// test/verify/phase2.sh verbatim so swapping the verifier to
	// rely on Category produces zero behavioural drift.
	CycleCategoryBootstrapCert CycleCategory = "bootstrap-cert"

	// CycleCategoryIntentional tags a cycle the operator explicitly
	// declared. Any member carrying
	// metadata.annotations["kubeatlas.io/intentional-cycle"] = "true"
	// suffices — one annotated node in the cycle is enough; we do
	// not require all members to opt in (it would be unergonomic
	// in multi-team setups where the cycle spans owners).
	CycleCategoryIntentional CycleCategory = "intentional"

	// CycleCategoryUnknown tags every cycle that does not match a
	// specific category yet. Verifiers should gate on unknown count
	// staying ≤ Phase 2 baseline; the GitHub Action maps unknown
	// → high-severity PR comment.
	CycleCategoryUnknown CycleCategory = "unknown"
)

// intentionalCycleAnnotation is the annotation key operators set
// to opt a deliberate cycle out of the "unknown" bucket. Kept as a
// named constant so an audit can grep for callers (extractor,
// classifier, docs).
const intentionalCycleAnnotation = "kubeatlas.io/intentional-cycle"

// classifyCycle picks the most-specific category that matches the
// given cycle. The precedence is bootstrap-cert > intentional >
// unknown — bootstrap-cert is structurally unmistakable, the
// annotation is a fallback opt-out for everything else.
//
// Anti-pattern guarded: the classifier is purely a function of the
// cycle's own Resources. It does not consult the GraphStore again
// to fetch fresh annotations. DetectCycles already operates on a
// store.Snapshot, so the annotations on Members are the same ones
// the cycle was detected from — re-fetching would just widen the
// race window.
func classifyCycle(members []graph.Resource) CycleCategory {
	if isBootstrapCertCycle(members) {
		return CycleCategoryBootstrapCert
	}
	for _, r := range members {
		if r.Annotations[intentionalCycleAnnotation] == "true" {
			return CycleCategoryIntentional
		}
	}
	return CycleCategoryUnknown
}

// isBootstrapCertCycle returns true when members forms the
// "controller owns its own cert Secret + consumes it" 2-cycle
// shape that test/verify/phase2.sh has been special-casing via JQ
// since Phase 2. The JQ check is:
//
//	(members[0].kind == "Secret" &&
//	 members[0].ownerReferences[*].name contains members[1].name)
//	|| (mirror with [1] and [0] swapped)
//
// This Go implementation matches that semantics byte-for-byte so
// the verifier can swap from JQ-filtering to category-checking
// without changing which cycles get accepted as benign.
//
// Note: matching is on owner name, not UID. UID would be stronger
// but introduces a divergence from the Phase 2 verifier. If a
// future version wants UID-based matching, change phase2.sh first
// and only then this function — the two must stay aligned.
func isBootstrapCertCycle(members []graph.Resource) bool {
	if len(members) != 2 {
		return false
	}
	return secretOwnsName(members[0], members[1]) || secretOwnsName(members[1], members[0])
}

// secretOwnsName reports whether maybeSecret is Kind=Secret and
// lists owner.Name among its OwnerReferences.
func secretOwnsName(maybeSecret, owner graph.Resource) bool {
	if maybeSecret.Kind != "Secret" {
		return false
	}
	for _, ref := range maybeSecret.OwnerReferences {
		if ref.Name == owner.Name {
			return true
		}
	}
	return false
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
		out = append(out, CycleReport{
			Members:  members,
			Category: classifyCycle(members),
		})
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

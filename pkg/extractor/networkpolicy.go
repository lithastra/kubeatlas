package extractor

import (
	"context"
	"fmt"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// NetworkPolicy edge extractors (P3-T1 / F-109).
//
// KubeAtlas surfaces three independent edge views for every
// NetworkPolicy:
//
//   * SELECTS_NP   — NetworkPolicy -> Pods that spec.podSelector
//                    matches in the policy's own namespace.
//   * ALLOWS_FROM  — NetworkPolicy -> Pods / Namespaces declared
//                    as sources in spec.ingress[].from[].
//   * ALLOWS_TO    — NetworkPolicy -> Pods / Namespaces declared
//                    as destinations in spec.egress[].to[].
//
// The edges reflect what the NetworkPolicy *declares*, not what the
// CNI plugin actually enforces — declarative topology only, per the
// "topology, not runtime" guard in F-109's anti-pattern list.
//
// These are the only extractors that resolve targets by enumeration
// rather than by computing IDs from the resource itself, so they
// take a graph.ResourceLister and issue scoped list queries (the
// policy's namespace, or the Namespace kind) instead of receiving a
// materialised copy of the whole graph.
//
// Scope limits in this v0.1 implementation, documented so future
// contributors see them at code-review time:
//
//   1. Only spec.{podSelector,namespaceSelector}.matchLabels is
//      consulted; matchExpressions is ignored. This drops some
//      precision but lets us ship a useful first cut without
//      pulling in the full LabelSelector evaluator. A follow-up
//      can switch to k8s.io/apimachinery/pkg/labels.Selector.
//   2. ipBlock peers are skipped — CIDR ranges aren't K8s
//      resources, per the F-109 anti-pattern guard.
//   3. policyTypes is honoured strictly: ALLOWS_FROM only emits
//      when "Ingress" is in spec.policyTypes, ALLOWS_TO only when
//      "Egress" is. A missing or empty policyTypes emits no
//      traffic edges (no implicit inference). SELECTS_NP is
//      independent of policyTypes.
//   4. An empty / wildcard peer (`{}` — neither podSelector nor
//      namespaceSelector nor ipBlock) is skipped rather than
//      treated as "match every Pod in every Namespace" — the
//      edge cardinality would explode and the information value
//      is low.
//   5. An empty `from: []` or `to: []` list emits no edges. K8s
//      semantics for empty peer lists are CNI-dependent and the
//      F-109 anti-pattern guard explicitly forbids us choosing a
//      side.

// SelectsNPExtractor emits SELECTS_NP edges from a NetworkPolicy
// to every Pod (and Pod-template-carrying workload) in the same
// namespace whose labels match spec.podSelector.matchLabels.
//
// An empty podSelector ({}) matches every Pod in the namespace —
// that is the K8s semantics ("apply to all pods") and the right
// signal for KubeAtlas to surface "this NetworkPolicy governs
// every workload here".
type SelectsNPExtractor struct{}

func (SelectsNPExtractor) Type() graph.EdgeType { return graph.EdgeTypeSelectsNP }

func (SelectsNPExtractor) Extract(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "NetworkPolicy" {
		return nil, nil
	}
	selector := nestedStringMap(r.Raw, "spec", "podSelector", "matchLabels")
	// podSelector resolves only against the policy's own namespace.
	candidates, err := q.ListResources(ctx, graph.Filter{Namespace: r.Namespace})
	if err != nil {
		return nil, fmt.Errorf("SelectsNPExtractor: list namespace %q: %w", r.Namespace, err)
	}
	from := r.ID()
	var edges []graph.Edge
	for _, t := range candidates {
		if !podLikeMatches(t, selector) {
			continue
		}
		edges = append(edges, graph.Edge{From: from, To: t.ID(), Type: graph.EdgeTypeSelectsNP})
	}
	return edges, nil
}

// AllowsFromExtractor emits ALLOWS_FROM edges for each peer in
// spec.ingress[].from[]. The edge target is a Pod ID when the
// peer has a podSelector and a Namespace ID when the peer has
// only a namespaceSelector. Peers with neither are skipped.
type AllowsFromExtractor struct{}

func (AllowsFromExtractor) Type() graph.EdgeType { return graph.EdgeTypeAllowsFrom }

func (AllowsFromExtractor) Extract(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "NetworkPolicy" {
		return nil, nil
	}
	if !hasPolicyType(r, "Ingress") {
		return nil, nil
	}
	return extractPeerEdges(ctx, r, q, "ingress", "from", graph.EdgeTypeAllowsFrom)
}

// AllowsToExtractor mirrors AllowsFromExtractor for spec.egress[].to[].
type AllowsToExtractor struct{}

func (AllowsToExtractor) Type() graph.EdgeType { return graph.EdgeTypeAllowsTo }

func (AllowsToExtractor) Extract(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "NetworkPolicy" {
		return nil, nil
	}
	if !hasPolicyType(r, "Egress") {
		return nil, nil
	}
	return extractPeerEdges(ctx, r, q, "egress", "to", graph.EdgeTypeAllowsTo)
}

// extractPeerEdges walks spec.<ruleField>[].<peerField>[] (so
// "ingress"/"from" or "egress"/"to") and emits one edge per matching
// Pod or Namespace. Helper factored to keep the two direction-
// specific extractors a four-line shell.
func extractPeerEdges(ctx context.Context, r graph.Resource, q graph.ResourceLister, ruleField, peerField string, edgeType graph.EdgeType) ([]graph.Edge, error) {
	rules := nestedSlice(r.Raw, "spec", ruleField)
	if len(rules) == 0 {
		return nil, nil
	}
	from := r.ID()
	var edges []graph.Edge
	for _, rawRule := range rules {
		rule, ok := rawRule.(map[string]any)
		if !ok {
			continue
		}
		peers, _ := rule[peerField].([]any)
		// Empty peer list — F-109 anti-pattern guard: do not infer
		// "allow all" vs "deny all" from an empty list; emit nothing.
		if len(peers) == 0 {
			continue
		}
		for _, rawPeer := range peers {
			peer, ok := rawPeer.(map[string]any)
			if !ok {
				continue
			}
			pe, err := peerEdges(ctx, r, q, peer, from, edgeType)
			if err != nil {
				return nil, err
			}
			edges = append(edges, pe...)
		}
	}
	return edges, nil
}

// peerEdges produces edges for one NetworkPolicyPeer.
//
// Cases:
//
//   * ipBlock-only peer            → skip (not a K8s resource).
//   * empty peer ({})              → skip (cardinality explosion).
//   * podSelector only             → edges to Pods in r.Namespace
//                                    matching the selector.
//   * namespaceSelector only       → edges to Namespaces whose
//                                    labels match.
//   * podSelector + nsSelector     → edges to Pods in any matching
//                                    Namespace whose labels match
//                                    podSelector.
func peerEdges(ctx context.Context, r graph.Resource, q graph.ResourceLister, peer map[string]any, from string, edgeType graph.EdgeType) ([]graph.Edge, error) {
	hasIPBlock := peer["ipBlock"] != nil
	hasPodSel := peer["podSelector"] != nil
	hasNsSel := peer["namespaceSelector"] != nil

	// ipBlock-only peers describe external CIDRs; skip per F-109
	// anti-pattern guard. ipBlock combined with selectors is
	// non-standard K8s but defensive: still skip.
	if hasIPBlock && !hasPodSel && !hasNsSel {
		return nil, nil
	}
	// Wildcard / empty peer.
	if !hasPodSel && !hasNsSel {
		return nil, nil
	}

	podSel := nestedStringMap(peer, "podSelector", "matchLabels")
	nsSel := nestedStringMap(peer, "namespaceSelector", "matchLabels")

	// namespaceSelector only — edge target is each matching Namespace.
	if hasNsSel && !hasPodSel {
		namespaces, err := q.ListResources(ctx, graph.Filter{Kind: "Namespace"})
		if err != nil {
			return nil, fmt.Errorf("peerEdges: list namespaces: %w", err)
		}
		var edges []graph.Edge
		for _, t := range namespaces {
			if !matchLabelSelector(t.Labels, nsSel) {
				continue
			}
			edges = append(edges, graph.Edge{From: from, To: t.ID(), Type: edgeType})
		}
		return edges, nil
	}

	// podSelector only — edge target is each matching Pod in
	// r.Namespace (the policy's own namespace).
	if hasPodSel && !hasNsSel {
		candidates, err := q.ListResources(ctx, graph.Filter{Namespace: r.Namespace})
		if err != nil {
			return nil, fmt.Errorf("peerEdges: list namespace %q: %w", r.Namespace, err)
		}
		var edges []graph.Edge
		for _, t := range candidates {
			if !podLikeMatches(t, podSel) {
				continue
			}
			edges = append(edges, graph.Edge{From: from, To: t.ID(), Type: edgeType})
		}
		return edges, nil
	}

	// Both selectors — pods in matching namespaces whose labels
	// match podSelector. Resolve namespaces first (small set) then
	// list each matching namespace's resources.
	namespaces, err := q.ListResources(ctx, graph.Filter{Kind: "Namespace"})
	if err != nil {
		return nil, fmt.Errorf("peerEdges: list namespaces: %w", err)
	}
	var edges []graph.Edge
	for _, nsRes := range namespaces {
		if !matchLabelSelector(nsRes.Labels, nsSel) {
			continue
		}
		pods, err := q.ListResources(ctx, graph.Filter{Namespace: nsRes.Name})
		if err != nil {
			return nil, fmt.Errorf("peerEdges: list namespace %q: %w", nsRes.Name, err)
		}
		for _, t := range pods {
			if !podLikeMatches(t, podSel) {
				continue
			}
			edges = append(edges, graph.Edge{From: from, To: t.ID(), Type: edgeType})
		}
	}
	return edges, nil
}

// podLikeMatches reports whether t is a Pod or a workload carrying
// a Pod template, and its (Pod label or pod-template label) set
// satisfies sel. An empty sel matches every Pod-like resource —
// the K8s "podSelector: {}" semantics, applied via
// matchLabelSelector (NOT labelsMatch — see that helper's godoc).
func podLikeMatches(t graph.Resource, sel map[string]string) bool {
	switch {
	case t.Kind == "Pod":
		return matchLabelSelector(t.Labels, sel)
	case hasPodTemplate(t.Kind):
		labels := nestedStringMap(podTemplateMeta(t), "labels")
		return matchLabelSelector(labels, sel)
	default:
		return false
	}
}

// hasPolicyType reports whether spec.policyTypes explicitly lists
// the given value ("Ingress" or "Egress"). A missing or empty
// policyTypes returns false — implicit inference (per K8s defaults)
// is intentionally not supported here.
func hasPolicyType(r graph.Resource, want string) bool {
	types := nestedSlice(r.Raw, "spec", "policyTypes")
	for _, raw := range types {
		if s, ok := raw.(string); ok && s == want {
			return true
		}
	}
	return false
}

// matchLabelSelector implements K8s LabelSelector matching semantics:
// an empty selector matches every object (the spec's "match all"
// shorthand). NetworkPolicy.spec.podSelector and namespaceSelector
// both follow these semantics.
//
// This is intentionally distinct from labelsMatch (in spec.go) which
// is hardcoded to return false for empty want — that's
// Service.spec.selector "headless = no pods" semantics, which is the
// opposite of what NetworkPolicy needs.
func matchLabelSelector(have, want map[string]string) bool {
	if len(want) == 0 {
		return true
	}
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

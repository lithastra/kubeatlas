package extractor

import (
	"sort"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// makeNP builds a NetworkPolicy graph.Resource with the given raw
// spec. Factored out because every test in this file constructs
// one, and the literal map nesting is noisy to repeat.
func makeNP(ns, name string, spec map[string]any) graph.Resource {
	return graph.Resource{
		Kind:      "NetworkPolicy",
		Namespace: ns,
		Name:      name,
		Raw: map[string]any{
			"spec": spec,
		},
	}
}

// --- SELECTS_NP ----------------------------------------------------

func TestSelectsNP_HappyPath(t *testing.T) {
	np := makeNP("demo", "deny-default", map[string]any{
		"podSelector": map[string]any{
			"matchLabels": map[string]any{"app": "web"},
		},
	})
	all := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "web-1", Labels: map[string]string{"app": "web"}},
		{Kind: "Pod", Namespace: "demo", Name: "api-1", Labels: map[string]string{"app": "api"}},
		{Kind: "Pod", Namespace: "other", Name: "web-x", Labels: map[string]string{"app": "web"}},
	}
	got := (SelectsNPExtractor{}).Extract(np, all)
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1: %+v", len(got), got)
	}
	if got[0].To != "demo/Pod/web-1" || got[0].Type != graph.EdgeTypeSelectsNP {
		t.Errorf("edge = %+v, want demo/Pod/web-1 SELECTS_NP", got[0])
	}
}

func TestSelectsNP_EmptyPodSelectorMatchesAllInNamespace(t *testing.T) {
	// K8s spec: podSelector: {} selects every Pod in the namespace.
	// The extractor must mirror that semantics.
	np := makeNP("demo", "apply-to-all", map[string]any{
		"podSelector": map[string]any{},
	})
	all := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "a", Labels: map[string]string{"app": "a"}},
		{Kind: "Pod", Namespace: "demo", Name: "b", Labels: map[string]string{"app": "b"}},
		{Kind: "Pod", Namespace: "other", Name: "c", Labels: map[string]string{"app": "c"}},
	}
	got := (SelectsNPExtractor{}).Extract(np, all)
	if len(got) != 2 {
		t.Fatalf("got %d edges, want 2 (both demo pods): %+v", len(got), got)
	}
}

func TestSelectsNP_MatchesPodTemplateWorkloads(t *testing.T) {
	// SELECTS_NP should treat Pod-template-carrying workloads
	// (Deployment / StatefulSet / etc) the same way the existing
	// SELECTS extractor does — by looking at spec.template.metadata.
	np := makeNP("demo", "match-api", map[string]any{
		"podSelector": map[string]any{
			"matchLabels": map[string]any{"app": "api"},
		},
	})
	dep := graph.Resource{
		Kind: "Deployment", Namespace: "demo", Name: "api",
		Raw: map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"metadata": map[string]any{
						"labels": map[string]any{"app": "api"},
					},
				},
			},
		},
	}
	got := (SelectsNPExtractor{}).Extract(np, []graph.Resource{dep})
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1: %+v", len(got), got)
	}
	if got[0].To != "demo/Deployment/api" {
		t.Errorf("To = %q, want demo/Deployment/api", got[0].To)
	}
}

func TestSelectsNP_NotANetworkPolicy(t *testing.T) {
	// Defensive: even if Raw looks NetworkPolicy-ish, the rule
	// must only fire for Kind=NetworkPolicy.
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "decoy",
		Raw: map[string]any{
			"spec": map[string]any{
				"podSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			},
		},
	}
	if got := (SelectsNPExtractor{}).Extract(pod, nil); got != nil {
		t.Errorf("non-NetworkPolicy input emitted edges: %v", got)
	}
}

// --- ALLOWS_FROM / ALLOWS_TO --------------------------------------

func TestAllowsFrom_RequiresExplicitPolicyType(t *testing.T) {
	// F-109 anti-pattern guard: missing or empty policyTypes
	// must NOT cause implicit inference. Even with ingress rules
	// present, no ALLOWS_FROM edges should fire.
	np := makeNP("demo", "no-policy-types", map[string]any{
		"podSelector": map[string]any{},
		// policyTypes intentionally absent.
		"ingress": []any{
			map[string]any{
				"from": []any{
					map[string]any{
						"podSelector": map[string]any{
							"matchLabels": map[string]any{"role": "frontend"},
						},
					},
				},
			},
		},
	})
	all := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "fe", Labels: map[string]string{"role": "frontend"}},
	}
	if got := (AllowsFromExtractor{}).Extract(np, all); got != nil {
		t.Errorf("missing policyTypes should emit no ALLOWS_FROM edges, got %v", got)
	}
}

func TestAllowsFrom_PodSelectorOnlyHitsSameNamespace(t *testing.T) {
	np := makeNP("demo", "allow-fe", map[string]any{
		"policyTypes": []any{"Ingress"},
		"podSelector": map[string]any{},
		"ingress": []any{
			map[string]any{
				"from": []any{
					map[string]any{
						"podSelector": map[string]any{
							"matchLabels": map[string]any{"role": "frontend"},
						},
					},
				},
			},
		},
	})
	all := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "fe-1", Labels: map[string]string{"role": "frontend"}},
		{Kind: "Pod", Namespace: "demo", Name: "be-1", Labels: map[string]string{"role": "backend"}},
		{Kind: "Pod", Namespace: "other", Name: "fe-x", Labels: map[string]string{"role": "frontend"}},
	}
	got := (AllowsFromExtractor{}).Extract(np, all)
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1 (same-ns frontend pod): %+v", len(got), got)
	}
	if got[0].To != "demo/Pod/fe-1" || got[0].Type != graph.EdgeTypeAllowsFrom {
		t.Errorf("edge = %+v, want demo/Pod/fe-1 ALLOWS_FROM", got[0])
	}
}

func TestAllowsFrom_NamespaceSelectorOnlyHitsNamespaces(t *testing.T) {
	// When the peer has only a namespaceSelector, the edge target
	// is the matching Namespace itself — not pods inside it.
	// Keeps cardinality manageable for "any pod in this namespace"
	// peers.
	np := makeNP("demo", "from-trusted-ns", map[string]any{
		"policyTypes": []any{"Ingress"},
		"podSelector": map[string]any{},
		"ingress": []any{
			map[string]any{
				"from": []any{
					map[string]any{
						"namespaceSelector": map[string]any{
							"matchLabels": map[string]any{"trust": "high"},
						},
					},
				},
			},
		},
	})
	all := []graph.Resource{
		{Kind: "Namespace", Namespace: "", Name: "demo", Labels: map[string]string{"trust": "low"}},
		{Kind: "Namespace", Namespace: "", Name: "trusted", Labels: map[string]string{"trust": "high"}},
		{Kind: "Pod", Namespace: "trusted", Name: "p", Labels: map[string]string{"app": "x"}},
	}
	got := (AllowsFromExtractor{}).Extract(np, all)
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1 (trusted namespace): %+v", len(got), got)
	}
	if got[0].To != "/Namespace/trusted" {
		t.Errorf("To = %q, want /Namespace/trusted", got[0].To)
	}
}

func TestAllowsFrom_BothSelectorsHitsCrossNamespacePods(t *testing.T) {
	// Both podSelector + namespaceSelector → edges to Pods in
	// matching namespaces whose labels match podSelector.
	np := makeNP("demo", "from-fe-in-trusted", map[string]any{
		"policyTypes": []any{"Ingress"},
		"podSelector": map[string]any{},
		"ingress": []any{
			map[string]any{
				"from": []any{
					map[string]any{
						"namespaceSelector": map[string]any{
							"matchLabels": map[string]any{"trust": "high"},
						},
						"podSelector": map[string]any{
							"matchLabels": map[string]any{"role": "frontend"},
						},
					},
				},
			},
		},
	})
	all := []graph.Resource{
		{Kind: "Namespace", Namespace: "", Name: "trusted", Labels: map[string]string{"trust": "high"}},
		{Kind: "Namespace", Namespace: "", Name: "untrusted", Labels: map[string]string{"trust": "low"}},
		{Kind: "Pod", Namespace: "trusted", Name: "fe-good", Labels: map[string]string{"role": "frontend"}},
		{Kind: "Pod", Namespace: "trusted", Name: "be-good", Labels: map[string]string{"role": "backend"}},
		{Kind: "Pod", Namespace: "untrusted", Name: "fe-bad", Labels: map[string]string{"role": "frontend"}},
	}
	got := (AllowsFromExtractor{}).Extract(np, all)
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1 (trusted/fe-good only): %+v", len(got), got)
	}
	if got[0].To != "trusted/Pod/fe-good" {
		t.Errorf("To = %q, want trusted/Pod/fe-good", got[0].To)
	}
}

func TestAllowsFrom_IPBlockOnlyPeerIsSkipped(t *testing.T) {
	// F-109 anti-pattern: CIDRs aren't K8s resources; ipBlock-only
	// peers emit no edges.
	np := makeNP("demo", "from-cidr", map[string]any{
		"policyTypes": []any{"Ingress"},
		"podSelector": map[string]any{},
		"ingress": []any{
			map[string]any{
				"from": []any{
					map[string]any{
						"ipBlock": map[string]any{"cidr": "10.0.0.0/24"},
					},
				},
			},
		},
	})
	all := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "p", Labels: map[string]string{"role": "x"}},
	}
	if got := (AllowsFromExtractor{}).Extract(np, all); got != nil {
		t.Errorf("ipBlock-only peer should emit nothing, got %v", got)
	}
}

func TestAllowsFrom_EmptyFromListEmitsNothing(t *testing.T) {
	// F-109 anti-pattern: empty `from: []` semantics are CNI-
	// dependent; the extractor refuses to take a side.
	np := makeNP("demo", "ambiguous", map[string]any{
		"policyTypes": []any{"Ingress"},
		"podSelector": map[string]any{},
		"ingress": []any{
			map[string]any{"from": []any{}},
		},
	})
	all := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "p", Labels: map[string]string{"role": "x"}},
	}
	if got := (AllowsFromExtractor{}).Extract(np, all); got != nil {
		t.Errorf("empty from list should emit nothing, got %v", got)
	}
}

func TestAllowsFrom_EmptyPeerObjectIsSkipped(t *testing.T) {
	// Peer `{}` would semantically mean "any pod in any namespace",
	// which explodes cardinality. The extractor skips it.
	np := makeNP("demo", "wildcard", map[string]any{
		"policyTypes": []any{"Ingress"},
		"podSelector": map[string]any{},
		"ingress": []any{
			map[string]any{
				"from": []any{
					map[string]any{}, // empty peer
				},
			},
		},
	})
	all := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "p", Labels: map[string]string{"role": "x"}},
		{Kind: "Pod", Namespace: "other", Name: "q", Labels: map[string]string{"role": "x"}},
	}
	if got := (AllowsFromExtractor{}).Extract(np, all); got != nil {
		t.Errorf("empty peer should emit nothing, got %v", got)
	}
}

func TestAllowsTo_MirrorsAllowsFromForEgress(t *testing.T) {
	// Quick parity check: ALLOWS_TO should behave the same as
	// ALLOWS_FROM but walk spec.egress[].to[] and require
	// "Egress" in policyTypes.
	np := makeNP("demo", "egress-to-db", map[string]any{
		"policyTypes": []any{"Egress"},
		"podSelector": map[string]any{},
		"egress": []any{
			map[string]any{
				"to": []any{
					map[string]any{
						"podSelector": map[string]any{
							"matchLabels": map[string]any{"role": "db"},
						},
					},
				},
			},
		},
	})
	all := []graph.Resource{
		{Kind: "Pod", Namespace: "demo", Name: "db-1", Labels: map[string]string{"role": "db"}},
	}
	got := (AllowsToExtractor{}).Extract(np, all)
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1: %+v", len(got), got)
	}
	if got[0].Type != graph.EdgeTypeAllowsTo || got[0].To != "demo/Pod/db-1" {
		t.Errorf("edge = %+v, want demo/Pod/db-1 ALLOWS_TO", got[0])
	}
	// And the same fixture should yield ZERO ALLOWS_FROM edges
	// (egress-only policy, no Ingress in policyTypes).
	if from := (AllowsFromExtractor{}).Extract(np, all); from != nil {
		t.Errorf("egress-only policy should emit no ALLOWS_FROM, got %v", from)
	}
}

// --- Registration --------------------------------------------------

func TestDefault_RegistersNetworkPolicyExtractors(t *testing.T) {
	// Pins that Default() includes the three Phase 3 NetworkPolicy
	// extractors. Caught a regression if a future refactor
	// accidentally drops them from the registration list.
	reg := Default()
	want := map[graph.EdgeType]bool{
		graph.EdgeTypeSelectsNP:  false,
		graph.EdgeTypeAllowsFrom: false,
		graph.EdgeTypeAllowsTo:   false,
	}
	for _, e := range reg.extractors {
		if _, expected := want[e.Type()]; expected {
			want[e.Type()] = true
		}
	}
	var missing []string
	for typ, seen := range want {
		if !seen {
			missing = append(missing, string(typ))
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("Default() missing NetworkPolicy extractors: %v", missing)
	}
}

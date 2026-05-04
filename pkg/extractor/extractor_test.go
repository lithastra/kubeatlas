package extractor

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestRegistry_DefaultRegistersAllEightTypes(t *testing.T) {
	reg := Default()
	got := make(map[graph.EdgeType]bool)
	for _, e := range reg.extractors {
		got[e.Type()] = true
	}
	for _, want := range graph.AllEdgeTypes {
		if !got[want] {
			t.Errorf("Default() missing extractor for %q", want)
		}
	}
	if len(reg.extractors) != len(graph.AllEdgeTypes) {
		t.Errorf("Default() registered %d extractors, want %d", len(reg.extractors), len(graph.AllEdgeTypes))
	}
}

func TestRegistry_ExtractAllConcatenates(t *testing.T) {
	// A single Pod with one owner ref + one ConfigMap envFrom + an
	// implicit default SA should yield 3 edges via Default().
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "p",
		OwnerReferences: []graph.OwnerRef{
			{Kind: "ReplicaSet", Name: "rs", UID: types.UID("u")},
		},
		Raw: map[string]any{
			"spec": map[string]any{
				"containers": []any{
					map[string]any{
						"envFrom": []any{
							map[string]any{"configMapRef": map[string]any{"name": "cm"}},
						},
					},
				},
			},
		},
	}
	got := Default().ExtractAll(pod, []graph.Resource{pod})
	if len(got) != 3 {
		t.Errorf("got %d edges, want 3 (OWNS + USES_CONFIGMAP + USES_SERVICEACCOUNT); edges=%v", len(got), got)
	}

	types := make(map[graph.EdgeType]int)
	for _, e := range got {
		types[e.Type]++
	}
	for _, want := range []graph.EdgeType{graph.EdgeTypeOwns, graph.EdgeTypeUsesConfigMap, graph.EdgeTypeUsesServiceAccount} {
		if types[want] != 1 {
			t.Errorf("expected exactly 1 %q edge, got %d", want, types[want])
		}
	}
}

func TestRegistry_NewIsEmpty(t *testing.T) {
	r := New()
	if got := r.ExtractAll(graph.Resource{Kind: "Pod"}, nil); got != nil {
		t.Errorf("empty registry should emit nothing, got %v", got)
	}
}

func TestRegistry_RegisterAppendsInOrder(t *testing.T) {
	r := New()
	r.Register(OwnsExtractor{})
	r.Register(VolumeExtractor{})
	if r.extractors[0].Type() != graph.EdgeTypeOwns {
		t.Errorf("first registered = %q, want OWNS", r.extractors[0].Type())
	}
	if r.extractors[1].Type() != graph.EdgeTypeMountsVolume {
		t.Errorf("second registered = %q, want MOUNTS_VOLUME", r.extractors[1].Type())
	}
}

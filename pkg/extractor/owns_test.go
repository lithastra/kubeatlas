package extractor

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func TestOwns_NoOwnerRefsEmitsNothing(t *testing.T) {
	pod := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "lonely"}
	if got := (OwnsExtractor{}).Extract(pod, nil); got != nil {
		t.Errorf("expected nil edges for pod without owner refs, got %v", got)
	}
}

func TestOwns_SingleOwnerRefEmitsOneEdge(t *testing.T) {
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "web-abc",
		OwnerReferences: []graph.OwnerRef{
			{Kind: "ReplicaSet", Name: "web-rs", UID: types.UID("rs-uid")},
		},
	}
	got := (OwnsExtractor{}).Extract(pod, nil)
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1", len(got))
	}
	if got[0].From != "demo/Pod/web-abc" || got[0].To != "demo/ReplicaSet/web-rs" {
		t.Errorf("wrong direction: %+v (want demo/Pod/web-abc -> demo/ReplicaSet/web-rs)", got[0])
	}
	if got[0].Type != graph.EdgeTypeOwns {
		t.Errorf("type = %q, want OWNS", got[0].Type)
	}
}

func TestOwns_MultipleOwnerRefsEmitOneEdgeEach(t *testing.T) {
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "web",
		OwnerReferences: []graph.OwnerRef{
			{Kind: "ReplicaSet", Name: "rs-a", UID: types.UID("a")},
			{Kind: "ReplicaSet", Name: "rs-b", UID: types.UID("b")},
		},
	}
	got := (OwnsExtractor{}).Extract(pod, nil)
	if len(got) != 2 {
		t.Errorf("got %d edges, want 2", len(got))
	}
}

func TestOwns_TypeReturnsConstant(t *testing.T) {
	if got := (OwnsExtractor{}).Type(); got != graph.EdgeTypeOwns {
		t.Errorf("Type() = %q, want OWNS", got)
	}
}

func TestOwns_PreservesNamespace(t *testing.T) {
	// Owner is in the same namespace as the owned resource. KubeAtlas
	// does not chase cross-namespace owner references because K8s
	// owner refs are namespace-scoped.
	pod := graph.Resource{
		Kind: "Pod", Namespace: "demo", Name: "web",
		OwnerReferences: []graph.OwnerRef{
			{Kind: "ReplicaSet", Name: "rs", UID: types.UID("u")},
		},
	}
	got := (OwnsExtractor{}).Extract(pod, nil)
	if got[0].To != "demo/ReplicaSet/rs" {
		t.Errorf("expected To in 'demo' namespace, got %q", got[0].To)
	}
}

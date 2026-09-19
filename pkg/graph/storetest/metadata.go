package storetest

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// RunMetadataSnapshot pins the optional optimization on both built-in stores.
// All other fields, cross-namespace edges, and the stored Raw data must survive.
func RunMetadataSnapshot(t *testing.T, factory Factory) {
	t.Helper()
	s := factory(t)
	metadata, ok := s.(graph.MetadataSnapshotter)
	if !ok {
		t.Fatal("built-in store lacks metadata snapshot capability")
	}
	ctx := context.Background()
	empty, err := metadata.SnapshotMetadata(ctx)
	if err != nil || empty == nil || empty.Resources == nil || empty.Edges == nil || len(empty.Resources) != 0 || len(empty.Edges) != 0 {
		t.Fatalf("empty snapshot=%+v error=%v", empty, err)
	}
	a := graph.Resource{Kind: "ConfigMap", Namespace: "a", Name: "source", ClusterID: "east",
		UID: "source-uid", GroupVersion: "v1", ResourceVersion: "123",
		Labels: map[string]string{"team": "infra"}, Annotations: map[string]string{"kubeatlas.io/intentional-cycle": "true"},
		OwnerReferences: []graph.OwnerRef{{Kind: "Deployment", Name: "owner", UID: "owner-uid"}},
		Raw:             map[string]any{"spec": map[string]any{"synthetic": "payload"}}}
	b := graph.Resource{Kind: "ConfigMap", Namespace: "b", Name: "target", ClusterID: "west",
		Raw: map[string]any{"synthetic": "second payload"}}
	for _, r := range []graph.Resource{a, b} {
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	e := graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeUsesConfigMap, Attributes: map[string]string{"reason": "synthetic"}}
	if err := s.UpsertEdge(ctx, e); err != nil {
		t.Fatal(err)
	}
	full, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := metadata.SnapshotMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range full.Resources {
		full.Resources[i].Raw = nil
	}
	sort.Slice(full.Resources, func(i, j int) bool { return full.Resources[i].ID() < full.Resources[j].ID() })
	sort.Slice(projected.Resources, func(i, j int) bool { return projected.Resources[i].ID() < projected.Resources[j].ID() })
	if !reflect.DeepEqual(full, projected) {
		t.Fatal("metadata snapshot changed fields other than Raw")
	}
	stored, err := s.GetResource(ctx, a.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.Raw, a.Raw) {
		t.Fatal("metadata snapshot changed the persisted Raw payload")
	}
}

package storetest

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Factory builds a fresh, empty GraphStore for a single sub-test.
// Each sub-test gets its own store so cases stay independent.
type Factory func(t *testing.T) graph.GraphStore

// Run exercises the GraphStore contract against a given implementation.
//
// The contract pins the semantics any GraphStore must honour:
// idempotent upserts, cascading delete of incident edges, exact-match
// label filtering, ErrNotFound for missing IDs, and that Snapshot
// returns the union of every resource and every edge.
func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("empty store has no resources", func(t *testing.T) {
		s := factory(t)
		got, err := s.ListResources(context.Background(), graph.Filter{})
		if err != nil {
			t.Fatalf("ListResources: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected 0 resources, got %d", len(got))
		}
	})

	t.Run("upsert then get", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		r := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web"}
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatalf("UpsertResource: %v", err)
		}
		got, err := s.GetResource(ctx, r.ID())
		if err != nil {
			t.Fatalf("GetResource: %v", err)
		}
		if got.Kind != "Deployment" || got.Name != "web" || got.Namespace != "demo" {
			t.Errorf("got %+v, want deployment demo/web", got)
		}
	})

	t.Run("upsert is idempotent and overwrites", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		r := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web", Labels: map[string]string{"v": "1"}}
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatal(err)
		}
		r2 := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web", Labels: map[string]string{"v": "2"}}
		if err := s.UpsertResource(ctx, r2); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetResource(ctx, r2.ID())
		if err != nil {
			t.Fatal(err)
		}
		if got.Labels["v"] != "2" {
			t.Errorf("expected overwritten label v=2, got %v", got.Labels)
		}
		all, _ := s.ListResources(ctx, graph.Filter{})
		if len(all) != 1 {
			t.Errorf("expected 1 resource after idempotent upsert, got %d", len(all))
		}
	})

	t.Run("get non-existent returns ErrNotFound", func(t *testing.T) {
		s := factory(t)
		_, err := s.GetResource(context.Background(), "demo/Deployment/missing")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var nf graph.ErrNotFound
		if !errors.As(err, &nf) {
			t.Fatalf("expected ErrNotFound, got %T: %v", err, err)
		}
		if nf.ID != "demo/Deployment/missing" {
			t.Errorf("ErrNotFound.ID = %q, want demo/Deployment/missing", nf.ID)
		}
	})

	t.Run("delete removes resource and is silent on missing", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		r := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web"}
		_ = s.UpsertResource(ctx, r)
		if err := s.DeleteResource(ctx, r.ID()); err != nil {
			t.Fatalf("DeleteResource: %v", err)
		}
		if _, err := s.GetResource(ctx, r.ID()); !errors.As(err, new(graph.ErrNotFound)) {
			t.Errorf("expected ErrNotFound after delete, got %v", err)
		}
		// Deleting a missing id is a no-op.
		if err := s.DeleteResource(ctx, "demo/Deployment/missing"); err != nil {
			t.Errorf("delete-missing should be a no-op, got %v", err)
		}
	})

	t.Run("delete cascades to incoming and outgoing edges", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		dep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web"}
		cfg := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "app-config"}
		pod := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "web-abc"}
		_ = s.UpsertResource(ctx, dep)
		_ = s.UpsertResource(ctx, cfg)
		_ = s.UpsertResource(ctx, pod)
		_ = s.UpsertEdge(ctx, graph.Edge{From: dep.ID(), To: cfg.ID(), Type: graph.EdgeTypeUsesConfigMap})
		_ = s.UpsertEdge(ctx, graph.Edge{From: dep.ID(), To: pod.ID(), Type: graph.EdgeTypeOwns})

		if err := s.DeleteResource(ctx, dep.ID()); err != nil {
			t.Fatal(err)
		}
		cfgIn, _ := s.ListIncoming(ctx, cfg.ID())
		if len(cfgIn) != 0 {
			t.Errorf("ConfigMap should have 0 incoming edges after dep delete, got %d", len(cfgIn))
		}
		podIn, _ := s.ListIncoming(ctx, pod.ID())
		if len(podIn) != 0 {
			t.Errorf("Pod should have 0 incoming edges after dep delete, got %d", len(podIn))
		}
		depOut, _ := s.ListOutgoing(ctx, dep.ID())
		if len(depOut) != 0 {
			t.Errorf("deleted dep should have 0 outgoing edges, got %d", len(depOut))
		}
	})

	t.Run("upsert edge is idempotent on (from, to, type)", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web"})
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "cm"})
		e := graph.Edge{From: "demo/Deployment/web", To: "demo/ConfigMap/cm", Type: graph.EdgeTypeUsesConfigMap}
		_ = s.UpsertEdge(ctx, e)
		_ = s.UpsertEdge(ctx, e)
		out, _ := s.ListOutgoing(ctx, e.From)
		if len(out) != 1 {
			t.Errorf("expected 1 outgoing edge after duplicate upsert, got %d", len(out))
		}
	})

	t.Run("different edge types between same pair coexist", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Service", Namespace: "demo", Name: "web"})
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Pod", Namespace: "demo", Name: "web-abc"})
		e1 := graph.Edge{From: "demo/Service/web", To: "demo/Pod/web-abc", Type: graph.EdgeTypeSelects}
		e2 := graph.Edge{From: "demo/Service/web", To: "demo/Pod/web-abc", Type: graph.EdgeTypeRoutesTo}
		_ = s.UpsertEdge(ctx, e1)
		_ = s.UpsertEdge(ctx, e2)
		out, _ := s.ListOutgoing(ctx, e1.From)
		if len(out) != 2 {
			t.Errorf("expected 2 outgoing edges of different types, got %d", len(out))
		}
	})

	t.Run("delete edge removes only the targeted (from, to, type)", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Service", Namespace: "demo", Name: "web"})
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Pod", Namespace: "demo", Name: "web-abc"})
		_ = s.UpsertEdge(ctx, graph.Edge{From: "demo/Service/web", To: "demo/Pod/web-abc", Type: graph.EdgeTypeSelects})
		_ = s.UpsertEdge(ctx, graph.Edge{From: "demo/Service/web", To: "demo/Pod/web-abc", Type: graph.EdgeTypeRoutesTo})

		if err := s.DeleteEdge(ctx, "demo/Service/web", "demo/Pod/web-abc", graph.EdgeTypeSelects); err != nil {
			t.Fatal(err)
		}
		out, _ := s.ListOutgoing(ctx, "demo/Service/web")
		if len(out) != 1 {
			t.Fatalf("expected 1 remaining edge, got %d", len(out))
		}
		if out[0].Type != graph.EdgeTypeRoutesTo {
			t.Errorf("wrong edge survived: got %q, want ROUTES_TO", out[0].Type)
		}
		if err := s.DeleteEdge(ctx, "demo/Service/web", "demo/Pod/web-abc", graph.EdgeTypeSelects); err != nil {
			t.Errorf("delete-missing-edge should be a no-op, got %v", err)
		}
	})

	t.Run("filter by namespace and kind", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web"})
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Deployment", Namespace: "other", Name: "api"})
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Service", Namespace: "demo", Name: "web"})

		got, _ := s.ListResources(ctx, graph.Filter{Namespace: "demo"})
		if len(got) != 2 {
			t.Errorf("filter ns=demo: got %d, want 2", len(got))
		}
		got, _ = s.ListResources(ctx, graph.Filter{Kind: "Deployment"})
		if len(got) != 2 {
			t.Errorf("filter kind=Deployment: got %d, want 2", len(got))
		}
		got, _ = s.ListResources(ctx, graph.Filter{Namespace: "demo", Kind: "Deployment"})
		if len(got) != 1 || got[0].Name != "web" {
			t.Errorf("filter ns=demo kind=Deployment: got %+v, want [demo/Deployment/web]", got)
		}
	})

	t.Run("filter by labels is exact match", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Pod", Namespace: "demo", Name: "a", Labels: map[string]string{"app": "web", "tier": "frontend"}})
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Pod", Namespace: "demo", Name: "b", Labels: map[string]string{"app": "web"}})
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Pod", Namespace: "demo", Name: "c", Labels: map[string]string{"app": "api"}})

		got, _ := s.ListResources(ctx, graph.Filter{Labels: map[string]string{"app": "web"}})
		if len(got) != 2 {
			t.Errorf("label app=web: got %d, want 2", len(got))
		}
		got, _ = s.ListResources(ctx, graph.Filter{Labels: map[string]string{"app": "web", "tier": "frontend"}})
		if len(got) != 1 || got[0].Name != "a" {
			t.Errorf("label app=web,tier=frontend: got %+v, want [a]", got)
		}
	})

	t.Run("snapshot returns all resources and edges", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web"})
		_ = s.UpsertResource(ctx, graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "cm"})
		_ = s.UpsertEdge(ctx, graph.Edge{From: "demo/Deployment/web", To: "demo/ConfigMap/cm", Type: graph.EdgeTypeUsesConfigMap})

		snap, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Resources) != 2 {
			t.Errorf("snapshot resources: got %d, want 2", len(snap.Resources))
		}
		if len(snap.Edges) != 1 {
			t.Errorf("snapshot edges: got %d, want 1", len(snap.Edges))
		}

		ids := []string{snap.Resources[0].ID(), snap.Resources[1].ID()}
		sort.Strings(ids)
		want := []string{"demo/ConfigMap/cm", "demo/Deployment/web"}
		if ids[0] != want[0] || ids[1] != want[1] {
			t.Errorf("snapshot ids = %v, want %v", ids, want)
		}
	})

	t.Run("list incoming and outgoing on isolated node", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		r := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "lonely"}
		_ = s.UpsertResource(ctx, r)
		in, _ := s.ListIncoming(ctx, r.ID())
		out, _ := s.ListOutgoing(ctx, r.ID())
		if len(in) != 0 || len(out) != 0 {
			t.Errorf("isolated node: incoming=%d outgoing=%d, want 0,0", len(in), len(out))
		}
	})
}

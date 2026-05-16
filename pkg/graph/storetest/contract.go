package storetest

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

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

	t.Run("traverse outgoing returns reachable nodes excluding source", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		// A -> B -> C -> D, plus A -> E
		a := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "a"}
		b := graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "b"}
		c := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "c"}
		d := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "d"}
		e := graph.Resource{Kind: "Service", Namespace: "demo", Name: "e"}
		for _, r := range []graph.Resource{a, b, c, d, e} {
			_ = s.UpsertResource(ctx, r)
		}
		_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeOwns})
		_ = s.UpsertEdge(ctx, graph.Edge{From: b.ID(), To: c.ID(), Type: graph.EdgeTypeOwns})
		_ = s.UpsertEdge(ctx, graph.Edge{From: c.ID(), To: d.ID(), Type: graph.EdgeTypeUsesConfigMap})
		_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: e.ID(), Type: graph.EdgeTypeSelects})

		got, err := s.Traverse(ctx, a.ID(), graph.TraverseOptions{
			Direction: graph.DirectionOutgoing,
			MaxDepth:  5,
		})
		if err != nil {
			t.Fatalf("Traverse: %v", err)
		}
		if len(got) != 4 {
			t.Errorf("outgoing from a: got %d resources, want 4", len(got))
		}
		names := make(map[string]bool, len(got))
		for _, r := range got {
			names[r.Name] = true
		}
		for _, want := range []string{"b", "c", "d", "e"} {
			if !names[want] {
				t.Errorf("traverse outgoing missing %q (got %v)", want, names)
			}
		}
		if names["a"] {
			t.Errorf("traverse must exclude the source node, got %v", names)
		}
	})

	t.Run("traverse incoming powers blast-radius semantics", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		// A -> B -> C -> D: walking incoming from D yields {C, B, A}.
		a := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "a"}
		b := graph.Resource{Kind: "ReplicaSet", Namespace: "demo", Name: "b"}
		c := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "c"}
		d := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "d"}
		for _, r := range []graph.Resource{a, b, c, d} {
			_ = s.UpsertResource(ctx, r)
		}
		_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeOwns})
		_ = s.UpsertEdge(ctx, graph.Edge{From: b.ID(), To: c.ID(), Type: graph.EdgeTypeOwns})
		_ = s.UpsertEdge(ctx, graph.Edge{From: c.ID(), To: d.ID(), Type: graph.EdgeTypeUsesConfigMap})

		got, err := s.Traverse(ctx, d.ID(), graph.TraverseOptions{
			Direction: graph.DirectionIncoming,
			MaxDepth:  5,
		})
		if err != nil {
			t.Fatalf("Traverse: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("incoming from d: got %d resources, want 3 (a,b,c)", len(got))
		}
	})

	t.Run("traverse respects edge type filter", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		a := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "a"}
		b := graph.Resource{Kind: "Pod", Namespace: "demo", Name: "b"}
		c := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "c"}
		for _, r := range []graph.Resource{a, b, c} {
			_ = s.UpsertResource(ctx, r)
		}
		_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeOwns})
		_ = s.UpsertEdge(ctx, graph.Edge{From: a.ID(), To: c.ID(), Type: graph.EdgeTypeUsesConfigMap})

		owns, err := s.Traverse(ctx, a.ID(), graph.TraverseOptions{
			Direction: graph.DirectionOutgoing,
			MaxDepth:  3,
			EdgeTypes: []graph.EdgeType{graph.EdgeTypeOwns},
		})
		if err != nil {
			t.Fatalf("Traverse OWNS: %v", err)
		}
		if len(owns) != 1 || owns[0].Name != "b" {
			t.Errorf("OWNS-only traverse: got %+v, want [b]", owns)
		}
	})

	t.Run("traverse rejects invalid direction", func(t *testing.T) {
		s := factory(t)
		_ = s.UpsertResource(context.Background(), graph.Resource{Kind: "Pod", Namespace: "demo", Name: "x"})
		_, err := s.Traverse(context.Background(), "demo/Pod/x", graph.TraverseOptions{MaxDepth: 5})
		if err == nil {
			t.Error("expected error on empty direction, got nil")
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

	// ---------------------------------------------------------------
	// P3-T0a pushdown methods (May 2026). These three methods power
	// cluster + namespace aggregation without materialising the
	// entire store via Snapshot — see the godoc on each method on
	// pkg/graph/store.go for why.
	// ---------------------------------------------------------------

	t.Run("KindCountsByNamespace empty store returns empty non-nil map", func(t *testing.T) {
		s := factory(t)
		got, err := s.KindCountsByNamespace(context.Background())
		if err != nil {
			t.Fatalf("KindCountsByNamespace: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil empty map, got nil")
		}
		if len(got) != 0 {
			t.Errorf("expected 0 entries, got %d", len(got))
		}
	})

	t.Run("KindCountsByNamespace tallies by (namespace, kind)", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		// 2 Deployments + 3 Pods in demo; 1 Deployment in other;
		// 2 cluster-scoped (Namespace ""); empty kind never appears.
		fixture := []graph.Resource{
			{Kind: "Deployment", Namespace: "demo", Name: "a"},
			{Kind: "Deployment", Namespace: "demo", Name: "b"},
			{Kind: "Pod", Namespace: "demo", Name: "p1"},
			{Kind: "Pod", Namespace: "demo", Name: "p2"},
			{Kind: "Pod", Namespace: "demo", Name: "p3"},
			{Kind: "Deployment", Namespace: "other", Name: "c"},
			{Kind: "ClusterRole", Namespace: "", Name: "cluster-admin"},
			{Kind: "ClusterRole", Namespace: "", Name: "view"},
		}
		for _, r := range fixture {
			if err := s.UpsertResource(ctx, r); err != nil {
				t.Fatalf("UpsertResource %s: %v", r.ID(), err)
			}
		}
		got, err := s.KindCountsByNamespace(ctx)
		if err != nil {
			t.Fatalf("KindCountsByNamespace: %v", err)
		}
		want := map[string]map[string]int{
			"demo":  {"Deployment": 2, "Pod": 3},
			"other": {"Deployment": 1},
			"":      {"ClusterRole": 2},
		}
		for ns, kinds := range want {
			gotBucket, ok := got[ns]
			if !ok {
				t.Errorf("missing bucket for ns=%q", ns)
				continue
			}
			for k, n := range kinds {
				if gotBucket[k] != n {
					t.Errorf("ns=%q kind=%q got %d want %d", ns, k, gotBucket[k], n)
				}
			}
		}
		// And no spurious entries.
		for ns := range got {
			if _, ok := want[ns]; !ok {
				t.Errorf("unexpected ns bucket %q in result", ns)
			}
		}
	})

	t.Run("CrossNamespaceEdgeCounts groups by (from-ns, to-ns)", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		// Resources spread across two namespaces.
		demoDep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web"}
		demoCM1 := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "cfg1"}
		demoCM2 := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "cfg2"}
		otherSvc := graph.Resource{Kind: "Service", Namespace: "other", Name: "api"}
		for _, r := range []graph.Resource{demoDep, demoCM1, demoCM2, otherSvc} {
			if err := s.UpsertResource(ctx, r); err != nil {
				t.Fatalf("UpsertResource: %v", err)
			}
		}
		// 2 same-ns edges in demo, 1 cross-ns edge other → demo.
		edges := []graph.Edge{
			{From: demoDep.ID(), To: demoCM1.ID(), Type: "USES_CONFIGMAP"},
			{From: demoDep.ID(), To: demoCM2.ID(), Type: "USES_CONFIGMAP"},
			{From: otherSvc.ID(), To: demoDep.ID(), Type: "ROUTES_TO"},
		}
		for _, e := range edges {
			if err := s.UpsertEdge(ctx, e); err != nil {
				t.Fatalf("UpsertEdge: %v", err)
			}
		}
		// Edge with missing endpoint — must be dropped, not counted.
		if err := s.UpsertEdge(ctx, graph.Edge{
			From: demoDep.ID(),
			To:   "ghost/ConfigMap/missing",
			Type: "USES_CONFIGMAP",
		}); err != nil {
			t.Fatalf("UpsertEdge dangling: %v", err)
		}
		got, err := s.CrossNamespaceEdgeCounts(ctx)
		if err != nil {
			t.Fatalf("CrossNamespaceEdgeCounts: %v", err)
		}
		want := map[graph.NamespacePair]int{
			{From: "demo", To: "demo"}:   2,
			{From: "other", To: "demo"}:  1,
		}
		for k, want := range want {
			if got[k] != want {
				t.Errorf("pair %+v: got %d want %d", k, got[k], want)
			}
		}
		// Dangling edge must not appear under any namespace pair.
		for k := range got {
			if _, ok := want[k]; !ok {
				t.Errorf("unexpected pair in result: %+v", k)
			}
		}
	})

	t.Run("NamespaceSubgraph returns only in-namespace resources and edges", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		demoDep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "web"}
		demoCM := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "cfg"}
		otherSvc := graph.Resource{Kind: "Service", Namespace: "other", Name: "api"}
		for _, r := range []graph.Resource{demoDep, demoCM, otherSvc} {
			if err := s.UpsertResource(ctx, r); err != nil {
				t.Fatalf("UpsertResource: %v", err)
			}
		}
		// One in-ns edge + one cross-ns edge.
		if err := s.UpsertEdge(ctx, graph.Edge{From: demoDep.ID(), To: demoCM.ID(), Type: "USES_CONFIGMAP"}); err != nil {
			t.Fatalf("UpsertEdge: %v", err)
		}
		if err := s.UpsertEdge(ctx, graph.Edge{From: otherSvc.ID(), To: demoDep.ID(), Type: "ROUTES_TO"}); err != nil {
			t.Fatalf("UpsertEdge: %v", err)
		}
		g, err := s.NamespaceSubgraph(ctx, "demo")
		if err != nil {
			t.Fatalf("NamespaceSubgraph: %v", err)
		}
		if g == nil {
			t.Fatal("expected non-nil Graph, got nil")
		}
		// Resources: only the two demo resources.
		gotIDs := make(map[string]bool, len(g.Resources))
		for _, r := range g.Resources {
			gotIDs[r.ID()] = true
		}
		wantIDs := map[string]bool{demoDep.ID(): true, demoCM.ID(): true}
		for id := range wantIDs {
			if !gotIDs[id] {
				t.Errorf("missing resource %s in subgraph", id)
			}
		}
		for id := range gotIDs {
			if !wantIDs[id] {
				t.Errorf("unexpected resource %s in subgraph (cross-ns leakage)", id)
			}
		}
		// Edges: only the in-ns edge; cross-ns must be dropped because
		// the otherSvc endpoint is not in the demo namespace.
		if len(g.Edges) != 1 {
			t.Fatalf("expected 1 edge in subgraph, got %d: %+v", len(g.Edges), g.Edges)
		}
		if g.Edges[0].From != demoDep.ID() || g.Edges[0].To != demoCM.ID() {
			t.Errorf("got edge %+v, want %s → %s", g.Edges[0], demoDep.ID(), demoCM.ID())
		}
	})

	t.Run("NamespaceSubgraph on empty namespace returns empty non-nil graph", func(t *testing.T) {
		s := factory(t)
		// Seed something in a different ns to prove the filter works.
		_ = s.UpsertResource(context.Background(), graph.Resource{
			Kind: "Pod", Namespace: "other", Name: "p",
		})
		g, err := s.NamespaceSubgraph(context.Background(), "demo")
		if err != nil {
			t.Fatalf("NamespaceSubgraph: %v", err)
		}
		if g == nil {
			t.Fatal("expected non-nil Graph, got nil")
		}
		if len(g.Resources) != 0 || len(g.Edges) != 0 {
			t.Errorf("expected empty subgraph, got resources=%d edges=%d", len(g.Resources), len(g.Edges))
		}
	})

	// ---------------------------------------------------------------
	// P3-T2 snapshot history (F-111). AppendEvent / WriteSnapshotMeta
	// / QueryEvents. Event counts stay well under the memory store's
	// maxMemoryEvents (1000) ring-buffer cap so these cases hold for
	// both the durable postgres backend and the bounded memory stub.
	// ---------------------------------------------------------------

	t.Run("QueryEvents empty store returns empty non-nil slice", func(t *testing.T) {
		s := factory(t)
		got, err := s.QueryEvents(context.Background(),
			"", time.Unix(0, 0), time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("QueryEvents: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil empty slice, got nil")
		}
		if len(got) != 0 {
			t.Errorf("expected 0 events, got %d", len(got))
		}
	})

	t.Run("AppendEvent then QueryEvents round-trips the event", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		ts := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
		ev := graph.ResourceEvent{
			Timestamp:       ts,
			Namespace:       "demo",
			Kind:            "Deployment",
			UID:             "uid-1",
			Name:            "api",
			EventType:       graph.EventTypeAdd,
			ResourceVersion: "100",
			Data:            map[string]any{"replicas": float64(3)},
		}
		if err := s.AppendEvent(ctx, ev); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		got, err := s.QueryEvents(ctx, "demo", ts.Add(-time.Minute), ts.Add(time.Minute))
		if err != nil {
			t.Fatalf("QueryEvents: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 event, got %d", len(got))
		}
		g := got[0]
		if g.Kind != "Deployment" || g.Name != "api" || g.EventType != graph.EventTypeAdd {
			t.Errorf("event round-trip mismatch: %+v", g)
		}
		if g.ID == 0 {
			t.Error("store must assign a non-zero event ID")
		}
		// Data is a JSONB round-trip; the value must survive.
		if g.Data["replicas"] != float64(3) {
			t.Errorf("Data not preserved: %+v", g.Data)
		}
	})

	t.Run("QueryEvents filters by namespace", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		ts := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
		for _, ns := range []string{"demo", "demo", "other"} {
			if err := s.AppendEvent(ctx, graph.ResourceEvent{
				Timestamp: ts, Namespace: ns, Kind: "Pod", Name: "p",
				EventType: graph.EventTypeUpdate,
			}); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}
		demo, err := s.QueryEvents(ctx, "demo", ts.Add(-time.Minute), ts.Add(time.Minute))
		if err != nil {
			t.Fatalf("QueryEvents demo: %v", err)
		}
		if len(demo) != 2 {
			t.Errorf("namespace=demo: got %d events, want 2", len(demo))
		}
		all, err := s.QueryEvents(ctx, "", ts.Add(-time.Minute), ts.Add(time.Minute))
		if err != nil {
			t.Fatalf("QueryEvents all: %v", err)
		}
		if len(all) != 3 {
			t.Errorf("namespace='' (all): got %d events, want 3", len(all))
		}
	})

	t.Run("QueryEvents filters by time window", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		base := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
		// Three events at T+0, T+10m, T+20m.
		for i, off := range []time.Duration{0, 10 * time.Minute, 20 * time.Minute} {
			if err := s.AppendEvent(ctx, graph.ResourceEvent{
				Timestamp: base.Add(off), Namespace: "demo", Kind: "Pod",
				Name: string(rune('a' + i)), EventType: graph.EventTypeAdd,
			}); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}
		// Window [T+5m, T+15m] should catch only the middle event.
		got, err := s.QueryEvents(ctx, "demo", base.Add(5*time.Minute), base.Add(15*time.Minute))
		if err != nil {
			t.Fatalf("QueryEvents: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("window [T+5m,T+15m]: got %d events, want 1", len(got))
		}
		if got[0].Name != "b" {
			t.Errorf("expected the T+10m event 'b', got %q", got[0].Name)
		}
	})

	t.Run("QueryEvents returns events oldest-first", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		base := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
		// Insert out of chronological order; QueryEvents must sort.
		for _, off := range []time.Duration{20 * time.Minute, 0, 10 * time.Minute} {
			if err := s.AppendEvent(ctx, graph.ResourceEvent{
				Timestamp: base.Add(off), Namespace: "demo", Kind: "Pod",
				Name: "p", EventType: graph.EventTypeAdd,
			}); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}
		got, err := s.QueryEvents(ctx, "demo", base.Add(-time.Hour), base.Add(time.Hour))
		if err != nil {
			t.Fatalf("QueryEvents: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d events, want 3", len(got))
		}
		for i := 1; i < len(got); i++ {
			if got[i].Timestamp.Before(got[i-1].Timestamp) {
				t.Errorf("events not oldest-first at index %d: %v before %v",
					i, got[i].Timestamp, got[i-1].Timestamp)
			}
		}
	})

	t.Run("WriteSnapshotMeta accepts each trigger kind", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		for _, trig := range []graph.SnapshotTrigger{
			graph.SnapshotTriggerPeriodic,
			graph.SnapshotTriggerManual,
			graph.SnapshotTriggerStartup,
		} {
			if err := s.WriteSnapshotMeta(ctx, graph.SnapshotMeta{
				ResourceCount: 42,
				EdgeCount:     17,
				DurationMS:    123,
				Trigger:       trig,
			}); err != nil {
				t.Errorf("WriteSnapshotMeta(%s): %v", trig, err)
			}
		}
	})

	t.Run("PruneEventsBefore deletes only events older than the cutoff", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		base := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
		// Events at T+0, T+1h, T+2h.
		for i, off := range []time.Duration{0, time.Hour, 2 * time.Hour} {
			if err := s.AppendEvent(ctx, graph.ResourceEvent{
				Timestamp: base.Add(off), Namespace: "demo", Kind: "Pod",
				Name: string(rune('a' + i)), EventType: graph.EventTypeAdd,
			}); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}
		// Cutoff at T+90m: the T+0 and T+1h events are older, the
		// T+2h event survives.
		deleted, err := s.PruneEventsBefore(ctx, base.Add(90*time.Minute))
		if err != nil {
			t.Fatalf("PruneEventsBefore: %v", err)
		}
		if deleted != 2 {
			t.Errorf("deleted = %d, want 2", deleted)
		}
		remaining, err := s.QueryEvents(ctx, "demo", base.Add(-time.Hour), base.Add(time.Hour*24))
		if err != nil {
			t.Fatalf("QueryEvents: %v", err)
		}
		if len(remaining) != 1 || remaining[0].Name != "c" {
			t.Errorf("after prune got %d events %v, want only the T+2h event 'c'",
				len(remaining), remaining)
		}
	})

	t.Run("PruneEventsBefore on empty store deletes nothing", func(t *testing.T) {
		s := factory(t)
		deleted, err := s.PruneEventsBefore(context.Background(), time.Now())
		if err != nil {
			t.Fatalf("PruneEventsBefore: %v", err)
		}
		if deleted != 0 {
			t.Errorf("deleted = %d, want 0 on an empty store", deleted)
		}
	})
}

package aggregator_test

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// seed builds a small petclinic-like store: one Deployment owning a
// ReplicaSet owning two Pods, a Service selecting the Deployment via
// label, a ConfigMap referenced by the Deployment, and a separate
// resource in another namespace so cluster-level aggregation can test
// cross-ns edges.
func seed(t *testing.T) graph.GraphStore {
	t.Helper()
	s := memory.New()
	ctx := context.Background()

	dep := graph.Resource{
		Kind: "Deployment", Namespace: "petclinic", Name: "api",
		UID:    types.UID("dep-uid"),
		Labels: map[string]string{"app": "api"},
	}
	rs := graph.Resource{
		Kind: "ReplicaSet", Namespace: "petclinic", Name: "api-rs",
		UID: types.UID("rs-uid"),
		OwnerReferences: []graph.OwnerRef{
			{Kind: "Deployment", Name: "api", UID: types.UID("dep-uid")},
		},
	}
	pod1 := graph.Resource{
		Kind: "Pod", Namespace: "petclinic", Name: "api-1",
		UID: types.UID("pod1-uid"),
		OwnerReferences: []graph.OwnerRef{
			{Kind: "ReplicaSet", Name: "api-rs", UID: types.UID("rs-uid")},
		},
	}
	pod2 := graph.Resource{
		Kind: "Pod", Namespace: "petclinic", Name: "api-2",
		UID: types.UID("pod2-uid"),
		OwnerReferences: []graph.OwnerRef{
			{Kind: "ReplicaSet", Name: "api-rs", UID: types.UID("rs-uid")},
		},
	}
	svc := graph.Resource{
		Kind: "Service", Namespace: "petclinic", Name: "api",
	}
	cm := graph.Resource{
		Kind: "ConfigMap", Namespace: "petclinic", Name: "app-config",
	}
	otherDep := graph.Resource{
		Kind: "Deployment", Namespace: "other", Name: "client",
	}

	for _, r := range []graph.Resource{dep, rs, pod1, pod2, svc, cm, otherDep} {
		if err := s.UpsertResource(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	// In-namespace edges.
	for _, e := range []graph.Edge{
		{From: pod1.ID(), To: rs.ID(), Type: graph.EdgeTypeOwns},
		{From: pod2.ID(), To: rs.ID(), Type: graph.EdgeTypeOwns},
		{From: rs.ID(), To: dep.ID(), Type: graph.EdgeTypeOwns},
		{From: dep.ID(), To: cm.ID(), Type: graph.EdgeTypeUsesConfigMap},
		{From: svc.ID(), To: pod1.ID(), Type: graph.EdgeTypeSelects},
		{From: svc.ID(), To: pod2.ID(), Type: graph.EdgeTypeSelects},
		// Cross-namespace edge for cluster-level aggregation.
		{From: otherDep.ID(), To: svc.ID(), Type: graph.EdgeTypeRoutesTo},
	} {
		if err := s.UpsertEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	return s
}

func TestClusterAggregator_OneNodePerNamespace(t *testing.T) {
	store := seed(t)
	view, err := (aggregator.ClusterAggregator{}).Aggregate(context.Background(), store, aggregator.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if view.Level != aggregator.LevelCluster {
		t.Errorf("Level = %q, want cluster", view.Level)
	}
	wantNamespaces := map[string]int{"petclinic": 6, "other": 1}
	if len(view.Nodes) != len(wantNamespaces) {
		t.Errorf("got %d namespace nodes, want %d", len(view.Nodes), len(wantNamespaces))
	}
	for _, n := range view.Nodes {
		want, ok := wantNamespaces[n.ID]
		if !ok {
			t.Errorf("unexpected namespace node %q", n.ID)
			continue
		}
		if n.Type != "aggregated" {
			t.Errorf("ns %q Type = %q, want aggregated", n.ID, n.Type)
		}
		if n.ChildrenCount != want {
			t.Errorf("ns %q ChildrenCount = %d, want %d", n.ID, n.ChildrenCount, want)
		}
		if len(n.ChildrenSummary) == 0 {
			t.Errorf("ns %q missing ChildrenSummary", n.ID)
		}
	}
}

func TestClusterAggregator_CrossNamespaceEdgeFolded(t *testing.T) {
	store := seed(t)
	view, _ := (aggregator.ClusterAggregator{}).Aggregate(context.Background(), store, aggregator.Scope{})

	if len(view.Edges) != 1 {
		t.Fatalf("got %d cross-ns edges, want 1; edges=%v", len(view.Edges), view.Edges)
	}
	e := view.Edges[0]
	if e.From != "other" || e.To != "petclinic" {
		t.Errorf("edge = %+v, want other -> petclinic", e)
	}
	if e.Count != 1 {
		t.Errorf("Count = %d, want 1", e.Count)
	}
}

func TestNamespaceAggregator_RequiresNamespace(t *testing.T) {
	_, err := (aggregator.NamespaceAggregator{}).Aggregate(context.Background(), seed(t), aggregator.Scope{})
	if err == nil {
		t.Fatal("expected error when Scope.Namespace is empty")
	}
}

func TestNamespaceAggregator_WorkloadsAndPassthroughs(t *testing.T) {
	store := seed(t)
	view, err := (aggregator.NamespaceAggregator{}).Aggregate(context.Background(), store, aggregator.Scope{Namespace: "petclinic"})
	if err != nil {
		t.Fatal(err)
	}

	// Expect: 2 aggregated workloads (Deployment/api, Service/api) +
	// 1 passthrough (ConfigMap/app-config) = 3 nodes.
	wantTypes := map[string]string{
		"petclinic/Deployment/api":       "aggregated",
		"petclinic/Service/api":          "aggregated",
		"petclinic/ConfigMap/app-config": "resource",
	}
	if len(view.Nodes) != len(wantTypes) {
		t.Errorf("got %d nodes, want %d; nodes=%+v", len(view.Nodes), len(wantTypes), view.Nodes)
	}
	for _, n := range view.Nodes {
		want, ok := wantTypes[n.ID]
		if !ok {
			t.Errorf("unexpected node %q", n.ID)
			continue
		}
		if n.Type != want {
			t.Errorf("node %q Type = %q, want %q", n.ID, n.Type, want)
		}
	}
}

func TestNamespaceAggregator_DeploymentChildrenCount(t *testing.T) {
	store := seed(t)
	view, _ := (aggregator.NamespaceAggregator{}).Aggregate(context.Background(), store, aggregator.Scope{Namespace: "petclinic"})

	for _, n := range view.Nodes {
		if n.ID == "petclinic/Deployment/api" {
			// 1 ReplicaSet + 2 Pods absorbed = ChildrenCount 3.
			if n.ChildrenCount != 3 {
				t.Errorf("Deployment/api ChildrenCount = %d, want 3", n.ChildrenCount)
			}
			return
		}
	}
	t.Error("Deployment/api node not found")
}

func TestNamespaceAggregator_PodEndpointsRewrittenToWorkload(t *testing.T) {
	store := seed(t)
	view, _ := (aggregator.NamespaceAggregator{}).Aggregate(context.Background(), store, aggregator.Scope{Namespace: "petclinic"})

	// Service/api -SELECTS-> Pod/api-1, Pod/api-2 should rewrite to a
	// single edge Service/api -SELECTS-> Deployment/api with Count 2.
	for _, e := range view.Edges {
		if e.From == "petclinic/Service/api" && e.Type == graph.EdgeTypeSelects {
			if e.To != "petclinic/Deployment/api" {
				t.Errorf("rewritten edge To = %q, want petclinic/Deployment/api", e.To)
			}
			if e.Count != 2 {
				t.Errorf("Count = %d, want 2", e.Count)
			}
			return
		}
	}
	t.Error("expected rewritten Service-SELECTS-Deployment edge not found")
}

func TestWorkloadAggregator_RequiresFullScope(t *testing.T) {
	store := seed(t)
	cases := []aggregator.Scope{
		{},
		{Namespace: "petclinic"},
		{Namespace: "petclinic", Kind: "Deployment"},
	}
	for _, sc := range cases {
		if _, err := (aggregator.WorkloadAggregator{}).Aggregate(context.Background(), store, sc); err == nil {
			t.Errorf("expected error for scope %+v, got nil", sc)
		}
	}
}

func TestWorkloadAggregator_PetClinicShape(t *testing.T) {
	store := seed(t)
	view, err := (aggregator.WorkloadAggregator{}).Aggregate(
		context.Background(), store,
		aggregator.Scope{Namespace: "petclinic", Kind: "Deployment", Name: "api"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if view.Level != aggregator.LevelWorkload {
		t.Errorf("Level = %q, want workload", view.Level)
	}
	// Expect: Deployment/api + ReplicaSet/api-rs + Pod/api-1 + Pod/api-2
	// + ConfigMap/app-config (referenced via Deployment) = 5 nodes.
	wantIDs := map[string]bool{
		"petclinic/Deployment/api":       false,
		"petclinic/ReplicaSet/api-rs":    false,
		"petclinic/Pod/api-1":            false,
		"petclinic/Pod/api-2":            false,
		"petclinic/ConfigMap/app-config": false,
	}
	for _, n := range view.Nodes {
		if _, ok := wantIDs[n.ID]; ok {
			wantIDs[n.ID] = true
		}
		if n.Type != "resource" {
			t.Errorf("workload-level node %q has Type=%q, want resource", n.ID, n.Type)
		}
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("missing expected node %q in workload view", id)
		}
	}
}

func TestWorkloadAggregator_RootMissingReturnsErrNotFound(t *testing.T) {
	store := seed(t)
	_, err := (aggregator.WorkloadAggregator{}).Aggregate(
		context.Background(), store,
		aggregator.Scope{Namespace: "petclinic", Kind: "Deployment", Name: "ghost"},
	)
	var nf graph.ErrNotFound
	if err == nil || !errors.As(err, &nf) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResourceAggregator_OneHopNeighbors(t *testing.T) {
	store := seed(t)
	view, err := (aggregator.ResourceAggregator{}).Aggregate(
		context.Background(), store,
		aggregator.Scope{Namespace: "petclinic", Kind: "Deployment", Name: "api"},
	)
	if err != nil {
		t.Fatal(err)
	}
	// api Deployment's neighbors: ReplicaSet api-rs (incoming OWNS),
	// ConfigMap app-config (outgoing USES_CONFIGMAP). Root + 2 = 3
	// nodes.
	if len(view.Nodes) != 3 {
		t.Errorf("expected 3 nodes (root + 2 neighbors), got %d: %+v", len(view.Nodes), view.Nodes)
	}
	if view.Truncated {
		t.Error("Truncated should be false for a 3-node view")
	}
}

func TestResourceAggregator_TruncatesAt30Neighbors(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	root := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "shared"}
	if err := store.UpsertResource(ctx, root); err != nil {
		t.Fatal(err)
	}
	// 50 incoming USES_CONFIGMAP edges from synthetic Deployments.
	for i := 0; i < 50; i++ {
		dep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: depName(i)}
		_ = store.UpsertResource(ctx, dep)
		_ = store.UpsertEdge(ctx, graph.Edge{From: dep.ID(), To: root.ID(), Type: graph.EdgeTypeUsesConfigMap})
	}
	view, err := (aggregator.ResourceAggregator{}).Aggregate(ctx, store,
		aggregator.Scope{Namespace: "demo", Kind: "ConfigMap", Name: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Truncated {
		t.Error("expected Truncated=true on >30-neighbor view")
	}
	// root + MaxResourceNeighbors == 31 nodes.
	if got, want := len(view.Nodes), aggregator.MaxResourceNeighbors+1; got != want {
		t.Errorf("expected %d nodes after truncation, got %d", want, got)
	}
}

func TestResourceAggregator_RootMissingReturnsErrNotFound(t *testing.T) {
	store := seed(t)
	_, err := (aggregator.ResourceAggregator{}).Aggregate(context.Background(), store,
		aggregator.Scope{Namespace: "petclinic", Kind: "Deployment", Name: "ghost"})
	var nf graph.ErrNotFound
	if err == nil || !errors.As(err, &nf) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// depName produces "dep-NNN" without using fmt, mirroring podName in
// the memory store tests.
func depName(i int) string {
	const digits = "0123456789"
	buf := []byte("dep-")
	if i == 0 {
		return string(append(buf, '0'))
	}
	var rev []byte
	for i > 0 {
		rev = append(rev, digits[i%10])
		i /= 10
	}
	for j := len(rev) - 1; j >= 0; j-- {
		buf = append(buf, rev[j])
	}
	return string(buf)
}

func TestNamespaceAggregator_EdgeCountInOut(t *testing.T) {
	store := seed(t)
	view, _ := (aggregator.NamespaceAggregator{}).Aggregate(context.Background(), store, aggregator.Scope{Namespace: "petclinic"})

	// Deployment/api should have:
	//   - in: 2 (Service/api -SELECTS->, after Pod-rewrite the
	//     ReplicaSet -OWNS-> Deployment edge survives too — but
	//     ReplicaSet is absorbed, so its outgoing edges get rewritten
	//     to Deployment/api -OWNS-> Deployment/api which is a self-
	//     loop. Self-loops count as both in and out.)
	for _, n := range view.Nodes {
		if n.ID == "petclinic/Deployment/api" {
			if n.EdgeCountIn == 0 {
				t.Error("Deployment/api EdgeCountIn = 0, want > 0")
			}
			return
		}
	}
}

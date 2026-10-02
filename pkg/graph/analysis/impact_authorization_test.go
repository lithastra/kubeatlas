// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

// The RoleBinding deliberately binds a ClusterRole in a different namespace
// from its ServiceAccount subject. Its grant scope is still the binding's NS.
func authorizationFixture() *graph.Graph {
	resources := []graph.Resource{
		{Kind: "ClusterRole", Name: "system:reader", GroupVersion: "rbac.authorization.k8s.io/v1", UID: "role-uid", ResourceVersion: "1"},
		{Kind: "RoleBinding", Namespace: "target", Name: "reader", GroupVersion: "rbac.authorization.k8s.io/v1", UID: "binding-uid", ResourceVersion: "2"},
		{Kind: "ServiceAccount", Namespace: "app", Name: "worker", GroupVersion: "v1", UID: "sa-uid", ResourceVersion: "3"},
		{Kind: "Deployment", Namespace: "app", Name: "api", GroupVersion: "apps/v1", UID: "workload-uid", ResourceVersion: "4"},
	}
	return &graph.Graph{Resources: resources, Edges: []graph.Edge{
		{From: resources[1].ID(), To: resources[0].ID(), Type: graph.EdgeTypeBindsRole},
		{From: resources[1].ID(), To: resources[2].ID(), Type: graph.EdgeTypeBindsSubject},
		{From: resources[3].ID(), To: resources[2].ID(), Type: graph.EdgeTypeUsesServiceAccount},
	}}
}

func authorizationIDs(result analysis.ImpactAuthorizationResult) []string {
	ids := make([]string, len(result.Resources))
	for i, match := range result.Resources {
		ids[i] = match.Resource.ID
	}
	return ids
}

func TestImpactAuthorization_MotifsPathsAndSeparateCounts(t *testing.T) {
	g := authorizationFixture()
	role, binding, account, workload := g.Resources[0].ID(), g.Resources[1].ID(), g.Resources[2].ID(), g.Resources[3].ID()
	for _, tc := range []struct {
		root     string
		ids      []string
		depths   []int
		ordinary int
		counts   analysis.ImpactAuthorizationCounts
	}{
		{role, []string{binding, account, workload}, []int{1, 2, 3}, 0, analysis.ImpactAuthorizationCounts{Total: 3, Direct: 1, Indirect: 2, Bindings: 1, ServiceAccounts: 1, Workloads: 1}},
		{binding, []string{role, account, workload}, []int{1, 1, 2}, 0, analysis.ImpactAuthorizationCounts{Total: 3, Direct: 2, Indirect: 1, Roles: 1, ServiceAccounts: 1, Workloads: 1}},
		{account, []string{workload, binding, role}, []int{1, 1, 2}, 1, analysis.ImpactAuthorizationCounts{Total: 3, Direct: 2, Indirect: 1, Roles: 1, Bindings: 1, Workloads: 1}},
		{workload, []string{account, binding, role}, []int{1, 2, 3}, 0, analysis.ImpactAuthorizationCounts{Total: 3, Direct: 1, Indirect: 2, Roles: 1, Bindings: 1, ServiceAccounts: 1}},
	} {
		t.Run(tc.root, func(t *testing.T) {
			result := requireImpact(t, g, tc.root, analysis.ImpactOptions{})
			got := result.Authorization
			if !slices.Equal(authorizationIDs(got), tc.ids) || got.Counts != tc.counts || result.Counts.Total != tc.ordinary {
				t.Fatalf("ordinary/authorization paths or counts changed: %+v", result)
			}
			if !got.Applicable || !got.ModeledTraversalComplete || got.Truncated || len(got.Notices) != 0 || got.EvidenceCategory != "stored_edge_association" || got.EffectivePermissions != "not_evaluated" || got.SourceVersionEvidence != "unknown" || got.ObservationCoverage != "unknown" {
				t.Fatalf("overstated evidence: %+v", got)
			}
			for i, match := range got.Resources {
				if match.Depth != tc.depths[i] || len(match.Path) != match.Depth || match.Path[0].TraversalFrom != tc.root || match.Path[len(match.Path)-1].TraversalTo != tc.ids[i] {
					t.Fatalf("broken path: %+v", match)
				}
				for j, step := range match.Path {
					if j > 0 && match.Path[j-1].TraversalTo != step.TraversalFrom {
						t.Fatal("path is not contiguous")
					}
					if step.Type == graph.EdgeTypeUsesServiceAccount {
						if step.From != workload || step.To != account || step.BindingScope != "" {
							t.Fatalf("workload edge changed: %+v", step)
						}
					} else if step.From != binding || step.BindingScope != "namespace" || step.BindingNamespace != "target" {
						t.Fatalf("binding scope/direction changed: %+v", step)
					}
				}
			}
			dependencies := requireImpact(t, g, tc.root, analysis.ImpactOptions{Relation: analysis.ImpactDependencies})
			if !reflect.DeepEqual(dependencies.Authorization, got) {
				t.Fatal("ordinary direction changed independent authorization facet")
			}
		})
	}
	// An independent, exact path oracle fixes all three storage/traversal directions.
	got := requireImpact(t, g, workload, analysis.ImpactOptions{}).Authorization
	want := []analysis.ImpactAuthorizationStep{
		{ImpactStep: analysis.ImpactStep{From: workload, To: account, Type: graph.EdgeTypeUsesServiceAccount, TraversalFrom: workload, TraversalTo: account}},
		{ImpactStep: analysis.ImpactStep{From: binding, To: account, Type: graph.EdgeTypeBindsSubject, TraversalFrom: account, TraversalTo: binding}, BindingScope: "namespace", BindingNamespace: "target"},
		{ImpactStep: analysis.ImpactStep{From: binding, To: role, Type: graph.EdgeTypeBindsRole, TraversalFrom: binding, TraversalTo: role}, BindingScope: "namespace", BindingNamespace: "target"},
	}
	if !reflect.DeepEqual(got.Resources[2].Path, want) {
		t.Fatalf("path=%+v want=%+v", got.Resources[2].Path, want)
	}
}

func TestImpactAuthorization_NoPeerExpansionAndStableRepresentative(t *testing.T) {
	g := authorizationFixture()
	role, binding, account, workload := g.Resources[0].ID(), g.Resources[1].ID(), g.Resources[2].ID(), g.Resources[3].ID()
	peer := graph.Resource{Kind: "Pod", Namespace: "app", Name: "peer"}
	otherSA := graph.Resource{Kind: "ServiceAccount", Namespace: "app", Name: "other"}
	otherBinding := graph.Resource{Kind: "RoleBinding", Namespace: "target", Name: "z-reader"}
	g.Resources = append(g.Resources, peer, otherSA, otherBinding)
	g.Edges = append(g.Edges,
		graph.Edge{From: peer.ID(), To: account, Type: graph.EdgeTypeUsesServiceAccount},
		graph.Edge{From: binding, To: otherSA.ID(), Type: graph.EdgeTypeBindsSubject},
		graph.Edge{From: otherBinding.ID(), To: role, Type: graph.EdgeTypeBindsRole},
		graph.Edge{From: otherBinding.ID(), To: otherSA.ID(), Type: graph.EdgeTypeBindsSubject},
		g.Edges[0], g.Edges[1])
	want := requireImpact(t, g, workload, analysis.ImpactOptions{})
	if !slices.Equal(authorizationIDs(want.Authorization), []string{account, binding, role}) {
		t.Fatalf("shared identity expanded to peers: %+v", want.Authorization)
	}
	fromAccount := requireImpact(t, g, account, analysis.ImpactOptions{}).Authorization
	if !slices.Equal(authorizationIDs(fromAccount), []string{workload, peer.ID(), binding, role}) || fromAccount.Counts.Pods != 1 {
		t.Fatalf("SA root expanded to sibling subjects/bindings: %+v", fromAccount)
	}
	// A second binding of the same account gives equal-length paths. Stable
	// neighbor order must retain reader, not z-reader, as the representative.
	g.Edges = append(g.Edges, graph.Edge{From: otherBinding.ID(), To: account, Type: graph.EdgeTypeBindsSubject})
	want = requireImpact(t, g, workload, analysis.ImpactOptions{})
	if want.Authorization.Resources[3].Path[1].From != binding {
		t.Fatal("wrong representative binding")
	}
	rng := rand.New(rand.NewPCG(17, 701))
	for range 100 {
		rng.Shuffle(len(g.Resources), func(i, j int) { g.Resources[i], g.Resources[j] = g.Resources[j], g.Resources[i] })
		rng.Shuffle(len(g.Edges), func(i, j int) { g.Edges[i], g.Edges[j] = g.Edges[j], g.Edges[i] })
		if got := requireImpact(t, g, workload, analysis.ImpactOptions{}); !reflect.DeepEqual(got, want) {
			t.Fatal("input permutation changed result")
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, err := analysis.AnalyzeImpact(context.Background(), g, workload, analysis.ImpactOptions{})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("concurrent read changed result: %v", err)
			}
		})
	}
	wg.Wait()
	want.Authorization.Resources[0].Path[0].From = "modified"
	if want.Authorization.Resources[3].Path[0].From != workload {
		t.Fatal("paths alias")
	}
}

func TestImpactAuthorization_IncompleteAndInvalidEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*graph.Graph)
		reason analysis.ImpactNoticeReason
		count  int
	}{
		{"missing role endpoint", func(g *graph.Graph) { g.Resources = g.Resources[1:] }, analysis.ImpactMissingResource, 2},
		{"missing role edge", func(g *graph.Graph) { g.Edges = g.Edges[1:] }, analysis.ImpactAuthorizationMissingRole, 2},
		{"ambiguous role", func(g *graph.Graph) {
			g.Edges = append(g.Edges, graph.Edge{From: g.Resources[1].ID(), To: "/ClusterRole/another", Type: graph.EdgeTypeBindsRole})
		}, analysis.ImpactAuthorizationAmbiguous, 2},
		{"ambiguous SA", func(g *graph.Graph) {
			g.Edges = append(g.Edges, graph.Edge{From: g.Resources[3].ID(), To: "app/ServiceAccount/another", Type: graph.EdgeTypeUsesServiceAccount})
		}, analysis.ImpactAuthorizationAmbiguous, 0},
		{"missing SA edge", func(g *graph.Graph) { g.Edges = g.Edges[:2] }, analysis.ImpactAuthorizationMissingSA, 0},
		{"wrong role group", func(g *graph.Graph) { g.Resources[0].GroupVersion = "custom/v1" }, analysis.ImpactAuthorizationInvalid, 2},
		{"cross namespace workload", func(g *graph.Graph) { g.Resources[3].Namespace = "elsewhere"; g.Edges[2].From = g.Resources[3].ID() }, analysis.ImpactAuthorizationInvalid, 0},
		{"cross namespace Role", func(g *graph.Graph) {
			g.Resources[0].Kind = "Role"
			g.Resources[0].Namespace = "elsewhere"
			g.Edges[0].To = g.Resources[0].ID()
		}, analysis.ImpactAuthorizationInvalid, 2},
		{"CRB cannot bind Role", func(g *graph.Graph) {
			g.Resources[0].Kind = "Role"
			g.Resources[0].Namespace = "target"
			g.Resources[1].Kind = "ClusterRoleBinding"
			g.Resources[1].Namespace = ""
			g.Edges[0].From = g.Resources[1].ID()
			g.Edges[0].To = g.Resources[0].ID()
			g.Edges[1].From = g.Resources[1].ID()
		}, analysis.ImpactAuthorizationInvalid, 2},
		{"cross cluster role", func(g *graph.Graph) { g.Resources[0].ClusterID = "foreign"; g.Edges[0].To = g.Resources[0].ID() }, analysis.ImpactCrossClusterExcluded, 2},
		{"wrong subject kind", func(g *graph.Graph) { g.Edges[1].To = g.Resources[3].ID() }, analysis.ImpactAuthorizationInvalid, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := authorizationFixture()
			tc.edit(g)
			root := "app/Deployment/api"
			if tc.name == "cross namespace workload" {
				root = "elsewhere/Deployment/api"
			}
			if tc.name == "wrong subject kind" {
				root = "target/RoleBinding/reader"
			}
			got := requireImpact(t, g, root, analysis.ImpactOptions{}).Authorization
			if got.Counts.Total != tc.count || !got.Counts.LowerBound || got.ModeledTraversalComplete || got.Truncated || !slices.ContainsFunc(got.Notices, func(n analysis.ImpactNotice) bool { return n.Reason == tc.reason }) {
				t.Fatalf("incomplete evidence was not explicit: %+v", got)
			}
		})
	}
}

func TestImpactAuthorization_ValidScopesAndUnsupportedSubjects(t *testing.T) {
	for _, clusterScoped := range []bool{false, true} {
		g := authorizationFixture()
		if clusterScoped {
			g.Resources[1].Kind, g.Resources[1].Namespace, g.Resources[1].Name = "ClusterRoleBinding", "", "system:reader"
		} else {
			g.Resources[0].Kind, g.Resources[0].Namespace = "Role", "target"
		}
		g.Edges[0].From, g.Edges[0].To, g.Edges[1].From = g.Resources[1].ID(), g.Resources[0].ID(), g.Resources[1].ID()
		g.Edges = append(g.Edges,
			graph.Edge{From: g.Resources[1].ID(), To: "/User/system:hidden-user", Type: graph.EdgeTypeBindsSubject},
			graph.Edge{From: g.Resources[1].ID(), To: "/Group/system:hidden-group", Type: graph.EdgeTypeBindsSubject},
			graph.Edge{From: g.Resources[1].ID(), To: "app/ServiceAccount/hidden-missing", Type: graph.EdgeTypeBindsSubject})
		got := requireImpact(t, g, g.Resources[0].ID(), analysis.ImpactOptions{}).Authorization
		if got.Counts.Total != 3 || !got.Counts.LowerBound || !reflect.DeepEqual(got.Notices, []analysis.ImpactNotice{{Reason: analysis.ImpactMissingResource, Count: 1}, {Reason: analysis.ImpactAuthorizationSubject, Count: 2}}) {
			t.Fatalf("subject limitations changed: %+v", got)
		}
		for _, step := range got.Resources[2].Path[:2] {
			if clusterScoped && (step.BindingScope != "cluster" || step.BindingNamespace != "") || !clusterScoped && (step.BindingScope != "namespace" || step.BindingNamespace != "target") {
				t.Fatalf("invalid binding scope: %+v", step)
			}
		}
		body, err := json.Marshal(got)
		if err != nil || strings.Contains(string(body), "hidden-") {
			t.Fatal("unresolved subject identity leaked")
		}
	}
}

func TestImpactAuthorization_LimitsAndNoPayload(t *testing.T) {
	g := authorizationFixture()
	for _, tc := range []struct {
		depth, limit, count int
		reasons             []string
	}{
		{2, 10, 2, []string{"max_depth"}}, {3, 10, 3, []string{}},
		{5, 2, 2, []string{"max_results"}}, {5, 3, 3, []string{}},
	} {
		result := requireImpact(t, g, g.Resources[0].ID(), analysis.ImpactOptions{MaxDepth: tc.depth, Limit: tc.limit})
		got := result.Authorization
		if got.Counts.Total != tc.count || !slices.Equal(got.TruncationReasons, tc.reasons) || got.Truncated != (len(tc.reasons) > 0) || got.Counts.LowerBound != got.Truncated || result.Counts.Total != 0 || result.Counts.LowerBound {
			t.Fatalf("independent limits changed: %+v", result)
		}
	}
	for i := range g.Resources {
		g.Resources[i].Raw = map[string]any{"rules": "synthetic-payload-canary", "roleRef": "synthetic-payload-canary"}
		g.Resources[i].Annotations = map[string]string{"test": "synthetic-payload-canary"}
	}
	g.Edges[0].Attributes = map[string]string{"test": "synthetic-payload-canary"}
	got := requireImpact(t, g, g.Resources[0].ID(), analysis.ImpactOptions{})
	body, err := json.Marshal(got)
	if err != nil || strings.Contains(string(body), "synthetic-payload-canary") || g.Resources[0].Raw["rules"] != "synthetic-payload-canary" {
		t.Fatal("payload escaped or input mutated")
	}
	for _, r := range []graph.Resource{{Kind: "ConfigMap", Namespace: "app", Name: "config"}, {Kind: "Role", Namespace: "app", Name: "role", GroupVersion: "custom/v1"}} {
		facet := requireImpact(t, &graph.Graph{Resources: []graph.Resource{r}}, r.ID(), analysis.ImpactOptions{}).Authorization
		if facet.Applicable || facet.ModeledTraversalComplete || facet.Reason != "unsupported_authorization_root" || facet.Counts.Total != 0 {
			t.Fatalf("unsupported root presented as complete RBAC: %+v", facet)
		}
	}
	// Synthetic oversized allowed identity: reject, never return a partial facet.
	g.Resources[1].Name = strings.Repeat("x", 1<<20)
	g.Edges[0].From, g.Edges[1].From = g.Resources[1].ID(), g.Resources[1].ID()
	result, err := analysis.AnalyzeImpact(context.Background(), g, g.Resources[0].ID(), analysis.ImpactOptions{})
	if result != nil || !errors.Is(err, analysis.ErrImpactAuthorizationLimit) {
		t.Fatalf("authorization output budget did not fail closed: %v", err)
	}
}

func TestImpactAuthorization_CancellationAndOneSnapshot(t *testing.T) {
	g := authorizationFixture()
	// Sweep every context checkpoint, including facet indexing and BFS. The
	// initial successful call counts checks without tying this test to line order.
	base, cancel := context.WithCancel(context.Background())
	ctx := &impactCancelContext{Context: base, cancel: cancel, remaining: 10000}
	if _, err := analysis.AnalyzeImpact(ctx, g, g.Resources[0].ID(), analysis.ImpactOptions{}); err != nil {
		t.Fatal(err)
	}
	checks := 10000 - ctx.remaining
	cancel()
	for checkpoint := 1; checkpoint <= checks; checkpoint++ {
		base, cancel := context.WithCancel(context.Background())
		ctx := &impactCancelContext{Context: base, cancel: cancel, remaining: checkpoint}
		got, err := analysis.AnalyzeImpact(ctx, g, g.Resources[0].ID(), analysis.ImpactOptions{})
		cancel()
		if got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("checkpoint %d returned partial success: %v", checkpoint, err)
		}
	}
	reads := 0
	probe := &impactSnapshotProbe{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) { reads++; return g, nil }}
	// The embedded GraphStore is nil: any fallback/extra per-resource query panics.
	got, err := analysis.AnalyzeStoredImpact(context.Background(), probe, g.Resources[0].ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{})
	if err != nil || reads != 1 || got.Authorization.Counts.Total != 3 {
		t.Fatalf("facet did not use the single projection: reads=%d error=%v", reads, err)
	}
}

func TestImpactAuthorization_NoUnmodeledExpansionOrCycles(t *testing.T) {
	g := authorizationFixture()
	role, binding, account := g.Resources[0].ID(), g.Resources[1].ID(), g.Resources[2].ID()
	other := graph.Resource{Kind: "Pod", Namespace: "app", Name: "not-associated"}
	g.Resources = append(g.Resources, other)
	for _, edgeType := range []graph.EdgeType{graph.EdgeTypeOwns, graph.EdgeTypeSelects, graph.EdgeTypeCallsAtRuntime, "CUSTOM"} {
		g.Edges = append(g.Edges, graph.Edge{From: other.ID(), To: account, Type: edgeType})
	}
	// A malformed self-loop is excluded and explained, not followed. The
	// ordinary traversal still has its own interpretation of other edge types.
	g.Edges = append(g.Edges, graph.Edge{From: binding, To: binding, Type: graph.EdgeTypeBindsSubject})
	got := requireImpact(t, g, role, analysis.ImpactOptions{}).Authorization
	if got.Counts.Total != 3 || !got.Counts.LowerBound || !reflect.DeepEqual(got.Notices, []analysis.ImpactNotice{{Reason: analysis.ImpactAuthorizationInvalid, Count: 1}}) {
		t.Fatalf("unmodeled edge or cycle entered the association motif: %+v", got)
	}
	if slices.Contains(authorizationIDs(got), role) || slices.Contains(authorizationIDs(got), other.ID()) {
		t.Fatal("root or unassociated resource was counted")
	}
}

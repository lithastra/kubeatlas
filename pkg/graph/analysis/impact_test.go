// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package analysis_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"k8s.io/apimachinery/pkg/types"
)

func TestAnalyzeImpact_ReferencePathsAndCounts(t *testing.T) {
	fixture := loadBlastReference(t)
	projection := &graph.Graph{Edges: fixture.Edges}
	for _, resource := range fixture.Resources {
		projection.Resources = append(projection.Resources, resource.Resource)
	}
	// Independent new-feature oracle, not the legacy query expectations. Every
	// path step preserves child -> owner / consumer -> dependency storage order.
	root := "demo/ConfigMap/settings"
	result := requireImpact(t, projection, root, analysis.ImpactOptions{})
	wantIDs := []string{"demo/Deployment/api", "demo/ReplicaSet/api-rs", "demo/Pod/api-pod", "demo/Service/api", "demo/Ingress/api"}
	wantTypes := []graph.EdgeType{graph.EdgeTypeUsesConfigMap, graph.EdgeTypeOwns, graph.EdgeTypeOwns, graph.EdgeTypeSelects, graph.EdgeTypeRoutesTo}
	if !slices.Equal(impactIDs(result), wantIDs) {
		t.Fatalf("dependents = %v, want %v", impactIDs(result), wantIDs)
	}
	var path []analysis.ImpactStep
	previous := root
	for i, id := range wantIDs {
		step := analysis.ImpactStep{From: id, To: previous, Type: wantTypes[i], TraversalFrom: previous, TraversalTo: id}
		if step.Type == graph.EdgeTypeOwns {
			step.OwnerUIDStatus = "matched"
		}
		path = append(path, step)
		if result.Resources[i].Depth != i+1 || !reflect.DeepEqual(result.Resources[i].Path, path) {
			t.Fatalf("unexpected shortest path for %s: %+v", id, result.Resources[i])
		}
		previous = id
	}
	wantCounts := analysis.ImpactCounts{Total: 5, Direct: 1, Indirect: 4, Workloads: 2, Pods: 1, Other: 2}
	if result.Counts != wantCounts || result.Truncated || !result.ModeledTraversalComplete || result.ObservationCoverage != "unknown" || len(result.Notices) != 0 {
		t.Fatalf("unexpected scope/counts: %+v", result)
	}
	if result.MaxDepth != 5 || result.Limit != 200 || result.Relation != analysis.ImpactDependents {
		t.Fatalf("defaults changed: %+v", result)
	}

	dependencies := requireImpact(t, projection, wantIDs[0], analysis.ImpactOptions{Relation: analysis.ImpactDependencies})
	wantDependencies := []string{root, "demo/PersistentVolumeClaim/data", "demo/Secret/credentials", "demo/ServiceAccount/worker"}
	if !slices.Equal(impactIDs(dependencies), wantDependencies) || dependencies.Counts != (analysis.ImpactCounts{Total: 4, Direct: 4, Other: 4}) {
		t.Fatalf("dependencies = %+v", dependencies)
	}
	for _, match := range dependencies.Resources {
		step := match.Path[0]
		if step.From != step.TraversalFrom || step.To != step.TraversalTo {
			t.Fatalf("dependencies reversed original edge: %+v", step)
		}
	}
	secret := dependencies.Resources[2].Resource
	if !secret.ReferenceOnly || secret.UID != "" || secret.ResourceVersion != "" {
		t.Fatalf("Secret must remain reference-only: %+v", secret)
	}
	role := requireImpact(t, projection, "demo/Role/reader", analysis.ImpactOptions{})
	if role.Counts.Total != 0 || !reflect.DeepEqual(role.Notices, []analysis.ImpactNotice{{Reason: analysis.ImpactAuthorizationSeparate, Count: 1}}) {
		t.Fatalf("RBAC must not become ordinary dependents: %+v", role)
	}
}

func TestAnalyzeImpact_DeterministicDiamondCyclesAndParallelEdges(t *testing.T) {
	projection, ids := impactTestGraph(4)
	// Synthetic topology for the traversal algorithm, not Kubernetes field proof:
	// root -> a,b; a,b -> c; c -> root; a -> a. c must get the path via a.
	projection.Edges = []graph.Edge{
		{From: ids[0], To: ids[2], Type: graph.EdgeTypeSelects},
		{From: ids[0], To: ids[1], Type: graph.EdgeTypeSelects},
		{From: ids[0], To: ids[1], Type: graph.EdgeTypeRoutesTo},
		{From: ids[1], To: ids[3], Type: graph.EdgeTypeAttachedTo},
		{From: ids[2], To: ids[3], Type: graph.EdgeTypeAttachedTo},
		{From: ids[3], To: ids[0], Type: graph.EdgeTypeAttachedTo},
		{From: ids[1], To: ids[1], Type: graph.EdgeTypeAttachedTo},
	}
	projection.Edges = append(projection.Edges, projection.Edges[2])
	opts := analysis.ImpactOptions{Relation: analysis.ImpactDependencies}
	expected := requireImpact(t, projection, ids[0], opts)
	if !slices.Equal(impactIDs(expected), ids[1:]) || expected.Counts.Total != 3 || expected.Truncated {
		t.Fatalf("cycles or duplicate edges corrupted result: %+v", expected)
	}
	wantPath := []analysis.ImpactStep{
		{From: ids[0], To: ids[1], Type: graph.EdgeTypeRoutesTo, TraversalFrom: ids[0], TraversalTo: ids[1]},
		{From: ids[1], To: ids[3], Type: graph.EdgeTypeAttachedTo, TraversalFrom: ids[1], TraversalTo: ids[3]},
	}
	if !reflect.DeepEqual(expected.Resources[2].Path, wantPath) {
		t.Fatalf("representative path = %+v, want %+v", expected.Resources[2].Path, wantPath)
	}
	rng := rand.New(rand.NewPCG(17, 71))
	for i := 0; i < 100; i++ {
		rng.Shuffle(len(projection.Resources), func(i, j int) {
			projection.Resources[i], projection.Resources[j] = projection.Resources[j], projection.Resources[i]
		})
		rng.Shuffle(len(projection.Edges), func(i, j int) { projection.Edges[i], projection.Edges[j] = projection.Edges[j], projection.Edges[i] })
		got := requireImpact(t, projection, ids[0], opts)
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("permutation %d changed result: %+v", i, got)
		}
	}
	// Path slices for separate matches must not alias.
	expected.Resources[0].Path[0].Type = "modified-result"
	if expected.Resources[2].Path[0].Type != graph.EdgeTypeRoutesTo {
		t.Fatal("one result path mutated another")
	}
}

func TestAnalyzeImpact_Truncation(t *testing.T) {
	projection, ids := impactTestGraph(4)
	for i := 0; i < 3; i++ {
		projection.Edges = append(projection.Edges, graph.Edge{From: ids[i], To: ids[i+1], Type: graph.EdgeTypeAttachedTo})
	}
	for _, tc := range []struct {
		name       string
		depth      int
		limit      int
		count      int
		truncation []string
	}{
		{"below depth", 2, 10, 2, []string{"max_depth"}},
		{"exact depth leaf", 3, 10, 3, []string{}},
		{"below result limit", 5, 2, 2, []string{"max_results"}},
		{"exact result limit", 5, 3, 3, []string{}},
		{"both exact", 3, 3, 3, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := requireImpact(t, projection, ids[0], analysis.ImpactOptions{Relation: analysis.ImpactDependencies, MaxDepth: tc.depth, Limit: tc.limit})
			truncated := len(tc.truncation) != 0
			if got.Counts.Total != tc.count || got.Truncated != truncated || got.Counts.LowerBound != truncated || got.ModeledTraversalComplete == truncated || !slices.Equal(got.TruncationReasons, tc.truncation) {
				t.Fatalf("unexpected bound result: %+v", got)
			}
		})
	}
	// A back edge from a node at the depth limit does not truncate the result.
	projection.Edges = append(projection.Edges, graph.Edge{From: ids[3], To: ids[0], Type: graph.EdgeTypeAttachedTo})
	got := requireImpact(t, projection, ids[0], analysis.ImpactOptions{Relation: analysis.ImpactDependencies, MaxDepth: 3})
	if got.Truncated {
		t.Fatal("visited back edge was counted as an undiscovered result")
	}
	// On a dense graph, limit still returns the deterministic first discoveries.
	dense, denseIDs := impactTestGraph(60)
	for _, from := range denseIDs {
		for _, to := range denseIDs {
			dense.Edges = append(dense.Edges, graph.Edge{From: from, To: to, Type: graph.EdgeTypeAttachedTo})
		}
	}
	got = requireImpact(t, dense, denseIDs[0], analysis.ImpactOptions{Limit: 3})
	if !slices.Equal(impactIDs(got), denseIDs[1:4]) || !slices.Equal(got.TruncationReasons, []string{"max_results"}) {
		t.Fatalf("dense graph result = %+v", got)
	}
}

func TestAnalyzeImpact_DefaultAndMaximumLimits(t *testing.T) {
	projection, ids := impactTestGraph(1002)
	for _, id := range ids[1:] {
		projection.Edges = append(projection.Edges, graph.Edge{From: id, To: ids[0], Type: graph.EdgeTypeAttachedTo})
	}
	for _, limit := range []int{0, 1000} {
		got := requireImpact(t, projection, ids[0], analysis.ImpactOptions{Limit: limit})
		want := limit
		if want == 0 {
			want = 200
		}
		if got.Counts.Total != want || !got.Truncated || !slices.Equal(impactIDs(got), ids[1:want+1]) {
			t.Fatalf("limit %d: count=%d truncated=%v", limit, got.Counts.Total, got.Truncated)
		}
	}
	chain, chainIDs := impactTestGraph(12)
	for i := 1; i < len(chainIDs); i++ {
		chain.Edges = append(chain.Edges, graph.Edge{From: chainIDs[i], To: chainIDs[i-1], Type: graph.EdgeTypeAttachedTo})
	}
	got := requireImpact(t, chain, chainIDs[0], analysis.ImpactOptions{MaxDepth: 10})
	if got.Counts.Total != 10 || got.Resources[9].Depth != 10 || !slices.Equal(got.TruncationReasons, []string{"max_depth"}) {
		t.Fatalf("maximum depth = %+v", got)
	}
}

func TestAnalyzeImpact_ClusterAndNamespaceScope(t *testing.T) {
	root := graph.Resource{Kind: "Service", Namespace: "app", Name: "backend", ClusterID: "prod"}
	consumer := graph.Resource{Kind: "HTTPRoute", Namespace: "gateway", Name: "public", ClusterID: "prod"}
	foreign := consumer
	foreign.ClusterID = "hidden"
	standalone := root
	standalone.ClusterID = ""
	projection := &graph.Graph{Resources: []graph.Resource{root, consumer, foreign, standalone}, Edges: []graph.Edge{
		{From: consumer.ID(), To: root.ID(), Type: graph.EdgeTypeRoutesTo},
		{From: foreign.ID(), To: root.ID(), Type: graph.EdgeTypeRoutesTo},
	}}
	got := requireImpact(t, projection, root.ID(), analysis.ImpactOptions{ClusterID: "prod"})
	if !slices.Equal(impactIDs(got), []string{consumer.ID()}) || !reflect.DeepEqual(got.Notices, []analysis.ImpactNotice{{Reason: analysis.ImpactCrossClusterExcluded, Count: 1}}) {
		t.Fatalf("cluster/namespace scope changed: %+v", got)
	}
	body, err := json.Marshal(got)
	if err != nil || strings.Contains(string(body), "hidden") {
		t.Fatalf("foreign identity escaped selected cluster, marshal error: %v", err)
	}
	local := requireImpact(t, projection, standalone.ID(), analysis.ImpactOptions{})
	if local.Counts.Total != 0 {
		t.Fatalf("empty cluster selected federation resources: %+v", local)
	}
	_, err = analysis.AnalyzeImpact(context.Background(), projection, foreign.ID(), analysis.ImpactOptions{ClusterID: "prod"})
	if !errors.Is(err, analysis.ErrImpactRootNotFound) {
		t.Fatalf("foreign root error = %v", err)
	}
}

func TestAnalyzeImpact_ExcludedAndUnresolvedRelationships(t *testing.T) {
	projection, ids := impactTestGraph(3)
	projection.Edges = []graph.Edge{
		{From: ids[1], To: ids[0], Type: graph.EdgeTypeBindsRole},
		{From: ids[1], To: ids[0], Type: graph.EdgeTypeBindsSubject},
		{From: ids[1], To: ids[0], Type: graph.EdgeTypeEnforces},
		{From: ids[1], To: ids[0], Type: graph.EdgeTypeCallsAtRuntime},
		{From: ids[1], To: ids[0], Type: "CUSTOM_RULE"},
		{From: "demo/Pod/missing", To: ids[0], Type: graph.EdgeTypeSelects},
		// Unreachable edges must not become a cluster-wide notice inventory.
		{From: "demo/Pod/also-missing", To: ids[2], Type: graph.EdgeTypeSelects},
	}
	projection.Edges = append(projection.Edges, projection.Edges[5])
	got := requireImpact(t, projection, ids[0], analysis.ImpactOptions{})
	want := []analysis.ImpactNotice{
		{Reason: analysis.ImpactAuthorizationSeparate, Count: 2},
		{Reason: analysis.ImpactUnmodeledRelationship, Count: 2},
		{Reason: analysis.ImpactRuntimeExcluded, Count: 1},
		{Reason: analysis.ImpactMissingResource, Count: 1},
	}
	if got.Counts.Total != 0 || !got.Counts.LowerBound || got.ModeledTraversalComplete || got.Truncated || !reflect.DeepEqual(got.Notices, want) {
		t.Fatalf("unresolved relationships must not be a complete empty result: %+v", got)
	}
	isolated := requireImpact(t, projection, ids[1], analysis.ImpactOptions{})
	if isolated.Counts.Total != 0 || !isolated.ModeledTraversalComplete || isolated.ObservationCoverage != "unknown" {
		t.Fatalf("empty graph cannot imply observation completeness: %+v", isolated)
	}
}

func TestAnalyzeImpact_OwnerInstanceEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ownerUID  types.UID
		refs      []graph.OwnerRef
		status    string
		notice    analysis.ImpactNoticeReason
		traversed bool
	}{
		{"matching UID", "new", []graph.OwnerRef{{Kind: "Deployment", Name: "api", UID: "new"}}, "matched", "", true},
		{"replacement owner", "new", []graph.OwnerRef{{Kind: "Deployment", Name: "api", UID: "old"}}, "", analysis.ImpactOwnerUIDMismatch, false},
		{"incomplete duplicate cannot mask replacement", "new", []graph.OwnerRef{{Kind: "Deployment", Name: "api", UID: "old"}, {Kind: "Deployment", Name: "api"}}, "", analysis.ImpactOwnerUIDMismatch, false},
		{"matching instance among references", "new", []graph.OwnerRef{{Kind: "Deployment", Name: "api", UID: "old"}, {Kind: "Deployment", Name: "api", UID: "new"}}, "matched", "", true},
		{"missing ref UID", "new", []graph.OwnerRef{{Kind: "Deployment", Name: "api"}}, "unknown", analysis.ImpactOwnerUIDUnknown, true},
		{"missing owner UID", "", []graph.OwnerRef{{Kind: "Deployment", Name: "api", UID: "old"}}, "unknown", analysis.ImpactOwnerUIDUnknown, true},
		{"missing source evidence", "new", nil, "unknown", analysis.ImpactOwnerUIDUnknown, true},
		{"unrelated owner ref", "new", []graph.OwnerRef{{Kind: "Deployment", Name: "other", UID: "new"}}, "unknown", analysis.ImpactOwnerUIDUnknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := graph.Resource{Kind: "Deployment", Name: "api", Namespace: "demo", UID: tc.ownerUID}
			child := graph.Resource{Kind: "ReplicaSet", Name: "api-rs", Namespace: "demo", OwnerReferences: tc.refs}
			projection := &graph.Graph{Resources: []graph.Resource{owner, child}, Edges: []graph.Edge{{From: child.ID(), To: owner.ID(), Type: graph.EdgeTypeOwns}}}
			for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependents, analysis.ImpactDependencies} {
				rootID := owner.ID()
				if relation == analysis.ImpactDependencies {
					rootID = child.ID()
				}
				got := requireImpact(t, projection, rootID, analysis.ImpactOptions{Relation: relation})
				if (len(got.Resources) == 1) != tc.traversed {
					t.Fatalf("wrong instance traversed: %+v", got)
				}
				if tc.traversed && got.Resources[0].Path[0].OwnerUIDStatus != tc.status {
					t.Fatalf("owner evidence = %+v", got.Resources[0].Path[0])
				}
				if tc.notice == "" && len(got.Notices) != 0 || tc.notice != "" && !reflect.DeepEqual(got.Notices, []analysis.ImpactNotice{{Reason: tc.notice, Count: 1}}) {
					t.Fatalf("notices = %+v", got.Notices)
				}
				if !tc.traversed && (got.ModeledTraversalComplete || !got.Counts.LowerBound) {
					t.Fatal("unresolved owner instance was presented as complete")
				}
			}
		})
	}
}

func TestAnalyzeImpact_IdentityAllowlistAndNoMutation(t *testing.T) {
	makeProjection := func() *graph.Graph {
		return &graph.Graph{Resources: []graph.Resource{
			{Kind: "Secret", Namespace: "demo", Name: "credentials", UID: "synthetic-secret-uid", GroupVersion: "synthetic-secret-group", ResourceVersion: "synthetic-secret-version",
				Raw: map[string]any{"data": "synthetic-secret-canary"}, Labels: map[string]string{"test": "synthetic-secret-label"},
				Annotations: map[string]string{"test": "synthetic-secret-annotation"}, OwnerReferences: []graph.OwnerRef{{Kind: "Pod", Name: "synthetic-secret-owner"}}},
			{Kind: "Deployment", Namespace: "demo", Name: "api", UID: "deployment-uid", GroupVersion: "apps/v1", ResourceVersion: "123",
				Raw: map[string]any{"spec": "synthetic-raw-canary"}, Annotations: map[string]string{"test": "synthetic-annotation-canary"}},
		}, Edges: []graph.Edge{{From: "demo/Deployment/api", To: "demo/Secret/credentials", Type: graph.EdgeTypeUsesSecret, Attributes: map[string]string{"test": "synthetic-edge-canary"}}}}
	}
	projection := makeProjection()
	got := requireImpact(t, projection, "demo/Secret/credentials", analysis.ImpactOptions{})
	if !reflect.DeepEqual(projection, makeProjection()) {
		t.Fatal("analysis mutated caller's projection")
	}
	if got.Root.UID != "" || got.Root.GroupVersion != "" || got.Root.ResourceVersion != "" || !got.Root.ReferenceOnly {
		t.Fatalf("Secret metadata escaped sanitizer: %+v", got.Root)
	}
	identity := got.Resources[0].Resource
	if identity.UID != "deployment-uid" || identity.ResourceVersion != "123" || identity.GroupVersion != "apps/v1" {
		t.Fatalf("non-Secret version identity lost: %+v", identity)
	}
	body, err := json.Marshal(got)
	if err != nil || strings.Contains(string(body), "synthetic-") {
		t.Fatalf("non-allowlisted metadata escaped result, marshal error: %v", err)
	}
	// These are synthetic test inputs only; no Kubernetes client or Secret read.
	_, err = analysis.AnalyzeImpact(context.Background(), projection, got.Root.ID, analysis.ImpactOptions{ExpectedUID: "synthetic-secret-uid"})
	if !errors.Is(err, analysis.ErrImpactUIDUnknown) {
		t.Fatalf("Secret UID was treated as observed: %v", err)
	}
}

func TestAnalyzeImpact_InputValidation(t *testing.T) {
	projection, ids := impactTestGraph(1)
	for _, tc := range []struct {
		name string
		opts analysis.ImpactOptions
	}{
		{"negative depth", analysis.ImpactOptions{MaxDepth: -1}},
		{"excessive depth", analysis.ImpactOptions{MaxDepth: 11}},
		{"negative limit", analysis.ImpactOptions{Limit: -1}},
		{"excessive limit", analysis.ImpactOptions{Limit: 1001}},
		{"unsupported relation", analysis.ImpactOptions{Relation: "upstream"}},
		{"ambiguous cluster", analysis.ImpactOptions{ClusterID: "prod:other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := analysis.AnalyzeImpact(context.Background(), projection, ids[0], tc.opts)
			if result != nil || !errors.Is(err, analysis.ErrImpactInvalidOptions) {
				t.Fatalf("invalid option accepted: result=%+v error=%v", result, err)
			}
		})
	}
	for _, tc := range []struct {
		name       string
		projection *graph.Graph
		root       string
		opts       analysis.ImpactOptions
		want       error
	}{
		{"empty root", projection, "", analysis.ImpactOptions{}, analysis.ErrImpactInvalidOptions},
		{"nil projection", nil, ids[0], analysis.ImpactOptions{}, analysis.ErrImpactInvalidProjection},
		{"missing root", &graph.Graph{}, ids[0], analysis.ImpactOptions{}, analysis.ErrImpactRootNotFound},
		{"missing UID", projection, ids[0], analysis.ImpactOptions{ExpectedUID: "old"}, analysis.ErrImpactUIDUnknown},
		{"malformed identity", &graph.Graph{Resources: []graph.Resource{{Kind: "Pod", Name: "a/b"}}}, ids[0], analysis.ImpactOptions{}, analysis.ErrImpactInvalidProjection},
		{"duplicate identity", &graph.Graph{Resources: []graph.Resource{projection.Resources[0], projection.Resources[0]}}, ids[0], analysis.ImpactOptions{}, analysis.ErrImpactInvalidProjection},
		{"API group collision", &graph.Graph{Resources: []graph.Resource{{Kind: "Rule", Name: "shared", GroupVersion: "one/v1"}, {Kind: "Rule", Name: "shared", GroupVersion: "two/v1"}}}, "/Rule/shared", analysis.ImpactOptions{}, analysis.ErrImpactInvalidProjection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := analysis.AnalyzeImpact(context.Background(), tc.projection, tc.root, tc.opts)
			if result != nil || !errors.Is(err, tc.want) {
				t.Fatalf("got result=%+v error=%v, want %v", result, err, tc.want)
			}
		})
	}
	projection.Resources[0].UID = "new"
	_, err := analysis.AnalyzeImpact(context.Background(), projection, ids[0], analysis.ImpactOptions{ExpectedUID: "old"})
	if !errors.Is(err, analysis.ErrImpactUIDMismatch) {
		t.Fatalf("replacement instance accepted: %v", err)
	}
	_ = requireImpact(t, projection, ids[0], analysis.ImpactOptions{ExpectedUID: "new"})
	// Cluster-scoped resources have an empty namespace, not an invalid identity.
	clusterScoped := &graph.Graph{Resources: []graph.Resource{{Kind: "Namespace", Name: "demo"}}}
	_ = requireImpact(t, clusterScoped, "/Namespace/demo", analysis.ImpactOptions{})
}

func TestAnalyzeImpact_CancellationAndDeadline(t *testing.T) {
	projection, ids := impactTestGraph(20)
	for _, id := range ids[1:] {
		projection.Edges = append(projection.Edges, graph.Edge{From: id, To: ids[0], Type: graph.EdgeTypeAttachedTo})
	}
	for _, tc := range []struct {
		name  string
		check int
	}{
		{"before work", 1},
		{"resource indexing", 5},
		{"edge indexing", 25},
		{"traversal", 45},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &impactCancelContext{Context: base, cancel: cancel, remaining: tc.check}
			result, err := analysis.AnalyzeImpact(ctx, projection, ids[0], analysis.ImpactOptions{})
			if result != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation became successful partial result: %+v %v", result, err)
			}
		})
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	result, err := analysis.AnalyzeImpact(ctx, projection, ids[0], analysis.ImpactOptions{})
	if result != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline became successful empty result: %+v %v", result, err)
	}
}

// Deterministically cancel during CPU work without timing-dependent sleeps.
type impactCancelContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *impactCancelContext) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestAnalyzeImpact_ConcurrentReadOnlyProjection(t *testing.T) {
	projection, ids := impactTestGraph(100)
	for i := 1; i < len(ids); i++ {
		projection.Edges = append(projection.Edges, graph.Edge{From: ids[i], To: ids[0], Type: graph.EdgeTypeAttachedTo})
	}
	want := requireImpact(t, projection, ids[0], analysis.ImpactOptions{})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got, err := analysis.AnalyzeImpact(context.Background(), projection, ids[0], analysis.ImpactOptions{})
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("concurrent immutable projection changed result: %v", err)
			}
		})
	}
	wg.Wait()
}

func TestAnalyzeImpact_ShortestDistancesAgainstRelaxationOracle(t *testing.T) {
	// An independent repeated-relaxation oracle checks shortest distances on
	// generated directed topologies. The hand-authored diamond test, separately,
	// fixes the representative-path tie breaker rather than deriving its oracle.
	rng := rand.New(rand.NewPCG(170, 1700))
	for trial := 0; trial < 40; trial++ {
		projection, ids := impactTestGraph(12)
		for range 25 {
			projection.Edges = append(projection.Edges, graph.Edge{From: ids[rng.IntN(len(ids))], To: ids[rng.IntN(len(ids))], Type: graph.EdgeTypeAttachedTo})
		}
		for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependents, analysis.ImpactDependencies} {
			distances := map[string]int{ids[0]: 0}
			for range len(ids) {
				for _, edge := range projection.Edges {
					from, to := edge.From, edge.To
					if relation == analysis.ImpactDependents {
						from, to = to, from
					}
					depth, reached := distances[from]
					previous, known := distances[to]
					if reached && (!known || previous > depth+1) {
						distances[to] = depth + 1
					}
				}
			}
			got := requireImpact(t, projection, ids[0], analysis.ImpactOptions{Relation: relation, MaxDepth: 10})
			wantCount := 0
			for id, depth := range distances {
				if id != ids[0] && depth <= 10 {
					wantCount++
				}
			}
			if len(got.Resources) != wantCount {
				t.Fatalf("trial %d %s: got %d results, want %d", trial, relation, len(got.Resources), wantCount)
			}
			for _, match := range got.Resources {
				depth, found := distances[match.Resource.ID]
				if !found || depth != match.Depth || len(match.Path) != depth {
					t.Fatalf("trial %d %s: non-shortest path %+v", trial, relation, match)
				}
				cursor := ids[0]
				for _, step := range match.Path {
					if step.TraversalFrom != cursor || !slices.ContainsFunc(projection.Edges, func(edge graph.Edge) bool {
						return edge.From == step.From && edge.To == step.To && edge.Type == step.Type
					}) {
						t.Fatalf("invented or disconnected path step: %+v", step)
					}
					cursor = step.TraversalTo
				}
				if cursor != match.Resource.ID {
					t.Fatalf("path ends at %s instead of %s", cursor, match.Resource.ID)
				}
			}
		}
	}
}

func impactTestGraph(count int) (*graph.Graph, []string) {
	projection := &graph.Graph{}
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		resource := graph.Resource{Kind: "Pod", Namespace: "demo", Name: fmt.Sprintf("node-%04d", i)}
		projection.Resources = append(projection.Resources, resource)
		ids = append(ids, resource.ID())
	}
	return projection, ids
}

func requireImpact(t *testing.T, projection *graph.Graph, root string, opts analysis.ImpactOptions) *analysis.ImpactResult {
	t.Helper()
	result, err := analysis.AnalyzeImpact(context.Background(), projection, root, opts)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func impactIDs(result *analysis.ImpactResult) []string {
	ids := make([]string, 0, len(result.Resources))
	for _, match := range result.Resources {
		ids = append(ids, match.Resource.ID)
	}
	return ids
}

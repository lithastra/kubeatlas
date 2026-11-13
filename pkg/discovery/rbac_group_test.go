// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCoreRoleRefGroupRecoveryAndAuthorization(t *testing.T) {
	for _, fixture := range []struct{ binding, role string }{{"RoleBinding", "Role"}, {"RoleBinding", "ClusterRole"}, {"ClusterRoleBinding", "ClusterRole"}} {
		for _, cluster := range []string{"east", ""} {
			for _, history := range []bool{false, true} {
				for _, state := range []string{"foreign-group", "malformed-group", "write-failure"} {
					t.Run(fmt.Sprintf("%s/%s/cluster=%s/history=%t/%s", fixture.binding, fixture.role, cluster, history, state), func(t *testing.T) {
						testRoleRefRecovery(t, fixture.binding, fixture.role, cluster, history, state)
					})
				}
			}
		}
	}
}

func testRoleRefRecovery(t *testing.T, bindingKind, roleKind, cluster string, history bool, state string) {
	t.Helper()
	ctx := context.Background()
	base := memory.New()
	store, sink := &resyncStore{GraphStore: base}, &resyncSink{}
	resource, bindingNS, roleNS, bindingScope := "rolebindings", "target", "", "namespace"
	if bindingKind == "ClusterRoleBinding" {
		resource, bindingNS, bindingScope = "clusterrolebindings", "", "cluster"
	}
	if roleKind == "Role" {
		roleNS = bindingNS
	}
	gvr := schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: resource}
	tracker := operations.NewCoverageTracker(time.Minute)
	token, err := tracker.Begin(cluster, []schema.GroupVersionResource{gvr}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token.InitialDeliveryComplete(gvr)
	token.ListResult(gvr, true, false, time.Now())
	token.WatchStarted(gvr)
	prior := time.Now().Add(-time.Minute)
	token.RecordGap(gvr, operations.CoverageProcessingFailed, prior)
	var notifications atomic.Int64
	m := NewInformerManager(nil, store, WithClusterID(cluster), WithExtractor(extractor.Default()), WithSnapshotSink(sink), WithMemberRepair(NewMemberRepair(nil, store, cluster)), WithBroadcaster(func(_, _, _, _ string) { notifications.Add(1) }))
	m.coverageSession = token
	if history {
		cancellationHistory(m, store, cluster, sink)
	}
	obj := resyncObject(bindingKind, "binding", "binding-uid", "version-one")
	obj.SetAPIVersion(gvr.GroupVersion().String())
	obj.SetNamespace(bindingNS)
	ref := map[string]any{"kind": roleKind, "name": "reader", "apiGroup": "rbac.authorization.k8s.io"}
	switch state {
	case "foreign-group":
		ref["apiGroup"] = "example.org"
	case "malformed-group":
		ref["apiGroup"] = map[string]any{"private": "role-group-canary"}
	case "roleRef-unsupported-kind":
		ref["kind"] = "Secret"
	case "roleRef-namespaced-role":
		ref["kind"] = "Role"
	case "roleRef-missing-name":
		delete(ref, "name")
	case "roleRef-malformed-kind":
		ref["kind"] = true
	}
	obj.Object["roleRef"] = ref
	switch state {
	case "roleRef-string":
		obj.Object["roleRef"] = "shape-canary"
	case "roleRef-array":
		obj.Object["roleRef"] = []any{ref}
	}
	obj.Object["subjects"] = []any{map[string]any{"kind": "ServiceAccount", "name": "worker", "namespace": "app", "apiGroup": ""}}
	r := UnstructuredToResource(obj, bindingKind)
	r.ClusterID = cluster
	account := graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "app", Name: "worker", GroupVersion: "v1", UID: "account-uid", ResourceVersion: "account-version"}
	workload := graph.Resource{ClusterID: cluster, Kind: "Deployment", Namespace: "app", Name: "consumer", GroupVersion: "apps/v1", UID: "workload-uid", ResourceVersion: "workload-version"}
	role := graph.Resource{ClusterID: cluster, Kind: roleKind, Namespace: roleNS, Name: "reader", GroupVersion: gvr.GroupVersion().String(), UID: "role-uid", ResourceVersion: "role-version"}
	for _, scope := range []string{"east", "west", ""} {
		for _, row := range []graph.Resource{account, workload, role} {
			row.ClusterID = scope
			if err := base.UpsertResource(ctx, row); err != nil {
				t.Fatal(err)
			}
		}
		a, w := account, workload
		a.ClusterID, w.ClusterID = scope, scope
		if err := base.UpsertEdge(ctx, graph.Edge{From: w.ID(), To: a.ID(), Type: graph.EdgeTypeUsesServiceAccount}); err != nil {
			t.Fatal(err)
		}
	}
	store.failEdge.Store(state == "write-failure")
	for _, event := range []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate} {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), event)
	}
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	wantEdges, wantAttempts, wantNotifications := 1, int64(2), int64(2)
	if state == "write-failure" {
		wantEdges, wantAttempts, wantNotifications = 0, 4, 1
	}
	if err != nil || len(edges) != wantEdges || store.edgeWrites.Load() != wantAttempts || store.resourceWrites.Load() != 1 || len(sink.recorded()) != 1 || notifications.Load() != wantNotifications {
		t.Fatal("role reference replay invented a role or lost subject/source receipts", edges, sink.recorded(), err)
	}
	if len(edges) == 1 && (edges[0].From != r.ID() || edges[0].To != account.ID() || edges[0].Type != graph.EdgeTypeBindsSubject) {
		t.Fatal("invalid role reference suppressed or changed the independent subject", edges)
	}
	before := coverageView(t, tracker, cluster, gvr)
	if before.ExtractionGapSince.IsZero() != (state == "write-failure") || before.PersistenceGapSince.IsZero() != (state != "write-failure") || !before.ProcessingGapSince.Equal(prior) {
		t.Fatal("role reference failure lost distinct gap evidence", before)
	}
	if state != "write-failure" {
		initial, err := analysis.AnalyzeStoredImpact(ctx, base, workload.ID(), analysis.ImpactOptions{ClusterID: cluster}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil || initial.Authorization.Counts.Total != 2 || !initial.Authorization.Counts.LowerBound || len(initial.Authorization.Notices) != 1 || initial.Authorization.Notices[0].Reason != analysis.ImpactAuthorizationMissingRole || initial.Authorization.Notices[0].Count != 1 {
			t.Fatal("same-name role concealed the invalid reference/missing role association", initial, err)
		}
	}
	wantSources := int64(1)
	if state != "write-failure" {
		// roleRef is immutable in Kubernetes. Model a fresh valid same-name
		// replacement, not an in-place API change to the invalid declaration.
		obj.SetUID("replacement-uid")
		obj.SetResourceVersion("version-two")
		ref["apiGroup"] = "rbac.authorization.k8s.io"
		ref["kind"], ref["name"] = roleKind, "reader"
		obj.Object["roleRef"] = ref
		wantSources = 2
	}
	store.failEdge.Store(false)
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 2 || store.resourceWrites.Load() != wantSources || store.edgeWrites.Load() != wantAttempts+4 || len(sink.recorded()) != int(wantSources) || notifications.Load() != wantNotifications+2 {
		t.Fatal("valid binding delivery/replay failed to recover without duplicate history", edges, sink.recorded(), err)
	}
	stored, err := base.GetResource(ctx, r.ID())
	if err != nil || stored.UID != obj.GetUID() || stored.ResourceVersion != obj.GetResourceVersion() {
		t.Fatal("binding recovery lost its committed identity", stored, err)
	}
	after := coverageView(t, tracker, cluster, gvr)
	if !after.ExtractionGapSince.Equal(before.ExtractionGapSince) || !after.PersistenceGapSince.Equal(before.PersistenceGapSince) || !after.ProcessingGapSince.Equal(prior) {
		t.Fatal("binding recovery erased known loss evidence", after)
	}
	wantPath := []analysis.ImpactAuthorizationStep{
		{ImpactStep: analysis.ImpactStep{From: workload.ID(), To: account.ID(), Type: graph.EdgeTypeUsesServiceAccount, TraversalFrom: workload.ID(), TraversalTo: account.ID()}},
		{ImpactStep: analysis.ImpactStep{From: r.ID(), To: account.ID(), Type: graph.EdgeTypeBindsSubject, TraversalFrom: account.ID(), TraversalTo: r.ID()}, BindingScope: bindingScope, BindingNamespace: bindingNS},
		{ImpactStep: analysis.ImpactStep{From: r.ID(), To: role.ID(), Type: graph.EdgeTypeBindsRole, TraversalFrom: r.ID(), TraversalTo: role.ID()}, BindingScope: bindingScope, BindingNamespace: bindingNS},
	}
	for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependencies, analysis.ImpactDependents} {
		result, err := analysis.AnalyzeStoredImpact(ctx, base, workload.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: relation}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil {
			t.Fatal(err)
		}
		auth := result.Authorization
		if auth.Counts.Total != 3 || auth.Counts.Direct != 1 || auth.Counts.Indirect != 2 || auth.Counts.Roles != 1 || auth.Counts.Bindings != 1 || auth.Counts.ServiceAccounts != 1 || auth.Counts.LowerBound || len(auth.Resources) != 3 || len(auth.Notices) != 0 || !auth.ModeledTraversalComplete || auth.EffectivePermissions != "not_evaluated" || auth.SourceVersionEvidence != "unknown" || auth.EvidenceCategory != "stored_edge_association" {
			t.Fatal("role reference recovery joined foreign data or overstated authorization evidence", auth)
		}
		for i, id := range []string{account.ID(), r.ID(), role.ID()} {
			if auth.Resources[i].Resource.ID != id || !reflect.DeepEqual(auth.Resources[i].Path, wantPath[:i+1]) {
				t.Fatal("role reference recovery changed exact association scope/direction", auth)
			}
		}
		wantOrdinary := 0
		if relation == analysis.ImpactDependencies {
			wantOrdinary = 1
		}
		if result.Counts.Total != wantOrdinary {
			t.Fatal("role associations changed ordinary impact counts", result)
		}
	}
}

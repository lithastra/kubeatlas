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

func TestCoreSubjectGroupRecoveryAndAuthorization(t *testing.T) {
	for _, fixture := range []struct{ binding, role string }{{"RoleBinding", "Role"}, {"RoleBinding", "ClusterRole"}, {"ClusterRoleBinding", "ClusterRole"}} {
		for _, cluster := range []string{"east", ""} {
			for _, history := range []bool{false, true} {
				for _, state := range []string{"foreign-group", "rbac-group", "malformed-group", "write-failure"} {
					t.Run(fmt.Sprintf("%s/%s/cluster=%s/history=%t/%s", fixture.binding, fixture.role, cluster, history, state), func(t *testing.T) {
						testSubjectGroupRecovery(t, fixture.binding, fixture.role, cluster, history, state)
					})
				}
			}
		}
	}
}

func testSubjectGroupRecovery(t *testing.T, bindingKind, roleKind, cluster string, history bool, state string) {
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
	subject := map[string]any{"kind": "ServiceAccount", "name": "worker", "namespace": "app", "apiGroup": ""}
	switch state {
	case "foreign-group":
		subject["apiGroup"] = "example.org"
	case "rbac-group":
		subject["apiGroup"] = "rbac.authorization.k8s.io"
	case "malformed-group":
		subject["apiGroup"] = map[string]any{"private": "subject-group-canary"}
	}
	obj.Object["subjects"] = []any{
		map[string]any{"kind": "ServiceAccount", "name": "prefix", "namespace": "app"},
		subject,
		map[string]any{"kind": "ServiceAccount", "name": "tail", "namespace": "app"},
	}
	obj.Object["roleRef"] = map[string]any{"kind": roleKind, "name": "reader", "apiGroup": "rbac.authorization.k8s.io"}
	r := UnstructuredToResource(obj, bindingKind)
	r.ClusterID = cluster
	account := graph.Resource{ClusterID: cluster, Kind: "ServiceAccount", Namespace: "app", Name: "worker", GroupVersion: "v1", UID: "account-uid", ResourceVersion: "account-version"}
	prefix, tail := account, account
	prefix.Name, prefix.UID, tail.Name, tail.UID = "prefix", "prefix-uid", "tail", "tail-uid"
	workload := graph.Resource{ClusterID: cluster, Kind: "Deployment", Namespace: "app", Name: "consumer", GroupVersion: "apps/v1", UID: "workload-uid", ResourceVersion: "workload-version"}
	role := graph.Resource{ClusterID: cluster, Kind: roleKind, Namespace: roleNS, Name: "reader", GroupVersion: gvr.GroupVersion().String(), UID: "role-uid", ResourceVersion: "role-version"}
	for _, scope := range []string{"east", "west", ""} {
		for _, row := range []graph.Resource{account, prefix, tail, workload, role} {
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
	wantEdges, wantAttempts, wantNotifications := 2, int64(4), int64(2)
	if state == "write-failure" {
		wantEdges, wantAttempts, wantNotifications = 0, 8, 1
	}
	if err != nil || len(edges) != wantEdges || store.edgeWrites.Load() != wantAttempts || store.resourceWrites.Load() != 1 || len(sink.recorded()) != 1 || notifications.Load() != wantNotifications {
		t.Fatal("subject group replay lost the confirmed prefix, role or source receipts", edges, sink.recorded(), err)
	}
	for _, edge := range edges {
		validPrefix := edge.Type == graph.EdgeTypeBindsSubject && edge.To == prefix.ID()
		validRole := edge.Type == graph.EdgeTypeBindsRole && edge.To == role.ID()
		if edge.From != r.ID() || !validPrefix && !validRole {
			t.Fatal("invalid subject group invented an account association or continued after failure", edges)
		}
	}
	before := coverageView(t, tracker, cluster, gvr)
	if before.ExtractionGapSince.IsZero() != (state == "write-failure") || before.PersistenceGapSince.IsZero() != (state != "write-failure") || !before.ProcessingGapSince.Equal(prior) {
		t.Fatal("subject group failure lost distinct gap evidence", before)
	}
	initial, err := analysis.AnalyzeStoredImpact(ctx, base, workload.ID(), analysis.ImpactOptions{ClusterID: cluster}, graph.ImpactSnapshotOptions{ClusterID: cluster})
	if err != nil || initial.Authorization.Counts.Total != 1 || initial.Authorization.Counts.ServiceAccounts != 1 || initial.Authorization.Counts.Bindings != 0 || initial.Authorization.Counts.Roles != 0 || len(initial.Authorization.Resources) != 1 || initial.Authorization.Resources[0].Resource.ID != account.ID() {
		t.Fatal("same-name account concealed the invalid group/missing binding association", initial, err)
	}
	wantSources := int64(1)
	if state != "write-failure" {
		// Subjects are mutable; retain the binding identity and immutable roleRef.
		obj.SetResourceVersion("version-two")
		subject["apiGroup"] = ""
		wantSources = 2
	}
	store.failEdge.Store(false)
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 4 || store.resourceWrites.Load() != wantSources || store.edgeWrites.Load() != wantAttempts+8 || len(sink.recorded()) != int(wantSources) || notifications.Load() != wantNotifications+2 {
		t.Fatal("valid subject update/replay failed to recover without duplicate history", edges, sink.recorded(), err)
	}
	stored, err := base.GetResource(ctx, r.ID())
	if err != nil || stored.UID != obj.GetUID() || stored.ResourceVersion != obj.GetResourceVersion() {
		t.Fatal("subject recovery lost its committed source identity", stored, err)
	}
	after := coverageView(t, tracker, cluster, gvr)
	if !after.ExtractionGapSince.Equal(before.ExtractionGapSince) || !after.PersistenceGapSince.Equal(before.PersistenceGapSince) || !after.ProcessingGapSince.Equal(prior) {
		t.Fatal("subject recovery erased known loss evidence", after)
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
			t.Fatal("subject recovery joined sibling/foreign accounts or overstated authorization evidence", auth)
		}
		for i, id := range []string{account.ID(), r.ID(), role.ID()} {
			if auth.Resources[i].Resource.ID != id || !reflect.DeepEqual(auth.Resources[i].Path, wantPath[:i+1]) {
				t.Fatal("subject recovery changed exact association scope/direction", auth)
			}
		}
		wantOrdinary := 0
		if relation == analysis.ImpactDependencies {
			wantOrdinary = 1
		}
		if result.Counts.Total != wantOrdinary {
			t.Fatal("subject associations changed ordinary impact counts", result)
		}
	}
}

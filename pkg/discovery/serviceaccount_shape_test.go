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

func TestCoreMalformedServiceAccountNameRecoveryAndAuthorization(t *testing.T) {
	for _, fixture := range []struct{ kind, group, resource string }{
		{"Pod", "", "pods"}, {"Deployment", "apps", "deployments"}, {"ReplicaSet", "apps", "replicasets"},
		{"StatefulSet", "apps", "statefulsets"}, {"DaemonSet", "apps", "daemonsets"}, {"Job", "batch", "jobs"}, {"CronJob", "batch", "cronjobs"},
	} {
		for _, cluster := range []string{"east", ""} {
			for _, history := range []bool{false, true} {
				for _, field := range []string{"serviceAccountName", "serviceAccount"} {
					t.Run(fmt.Sprintf("%s/cluster=%s/history=%t/%s", fixture.kind, cluster, history, field), func(t *testing.T) {
						gvr := schema.GroupVersionResource{Group: fixture.group, Version: "v1", Resource: fixture.resource}
						testAccountShapeRecovery(t, fixture.kind, gvr, cluster, history, field)
					})
				}
			}
		}
	}
}

func testAccountShapeRecovery(t *testing.T, kind string, gvr schema.GroupVersionResource, cluster string, history bool, field string) {
	t.Helper()
	ctx := context.Background()
	base := memory.New()
	store, sink := &resyncStore{GraphStore: base}, &resyncSink{}
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
	obj := resyncObject(kind, "consumer", "consumer-uid", "version-one")
	obj.SetAPIVersion(gvr.GroupVersion().String())
	spec := map[string]any{"serviceAccountName": "", "serviceAccount": "fallback", "automountServiceAccountToken": false, "containers": []any{map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "config"}}}}}}
	spec[field] = true
	outer := spec
	if kind == "CronJob" {
		outer = map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": spec}}}}
	} else if kind != "Pod" {
		outer = map[string]any{"template": map[string]any{"spec": spec}}
	}
	obj.Object["spec"] = outer
	r := UnstructuredToResource(obj, kind)
	r.ClusterID = cluster
	config := graph.Resource{ClusterID: cluster, Namespace: r.Namespace, Kind: "ConfigMap", Name: "config", GroupVersion: "v1", UID: "config-uid", ResourceVersion: "config-version"}
	if err := base.UpsertResource(ctx, config); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"east", "west", ""} {
		role := graph.Resource{ClusterID: scope, Namespace: r.Namespace, Kind: "Role", Name: "reader", GroupVersion: "rbac.authorization.k8s.io/v1", UID: "role-uid", ResourceVersion: "role-version"}
		if err := base.UpsertResource(ctx, role); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"default", "fallback", "selected"} {
			account := graph.Resource{ClusterID: scope, Namespace: r.Namespace, Kind: "ServiceAccount", Name: name, GroupVersion: "v1", UID: "account-uid", ResourceVersion: "account-version"}
			binding := graph.Resource{ClusterID: scope, Namespace: r.Namespace, Kind: "RoleBinding", Name: name + "-binding", GroupVersion: "rbac.authorization.k8s.io/v1", UID: "binding-uid", ResourceVersion: "binding-version"}
			for _, row := range []graph.Resource{account, binding} {
				if err := base.UpsertResource(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			for _, edge := range []graph.Edge{{From: binding.ID(), To: account.ID(), Type: graph.EdgeTypeBindsSubject}, {From: binding.ID(), To: role.ID(), Type: graph.EdgeTypeBindsRole}} {
				if err := base.UpsertEdge(ctx, edge); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, event := range []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate} {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), event)
	}
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 1 || edges[0].To != config.ID() || edges[0].Type != graph.EdgeTypeUsesConfigMap || store.resourceWrites.Load() != 1 || store.edgeWrites.Load() != 2 || len(sink.recorded()) != 1 || notifications.Load() != 2 {
		t.Fatal("malformed account replay invented a fallback or lost independent collection", edges, sink.recorded(), err)
	}
	before := coverageView(t, tracker, cluster, gvr)
	if before.ExtractionGapSince.IsZero() || !before.ProcessingGapSince.Equal(prior) || !before.PersistenceGapSince.IsZero() {
		t.Fatal("account shape failure lost distinct gap evidence", before)
	}
	initial, err := analysis.AnalyzeStoredImpact(ctx, base, r.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependencies}, graph.ImpactSnapshotOptions{ClusterID: cluster})
	if err != nil || initial.Counts.Total != 1 || initial.Authorization.Counts.Total != 0 || !initial.Authorization.Counts.LowerBound || len(initial.Authorization.Notices) != 1 || initial.Authorization.Notices[0].Reason != analysis.ImpactAuthorizationMissingSA || initial.Authorization.Notices[0].Count != 1 {
		t.Fatal("invalid account field concealed a missing association with default/fallback grants", initial, err)
	}
	obj.SetResourceVersion("version-two")
	spec[field] = "selected"
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 2 || store.resourceWrites.Load() != 2 || store.edgeWrites.Load() != 6 || len(sink.recorded()) != 2 || notifications.Load() != 4 {
		t.Fatal("valid account/replay failed to recover without duplicate resource history", edges, sink.recorded(), err)
	}
	stored, err := base.GetResource(ctx, r.ID())
	if err != nil || stored.ResourceVersion != "version-two" || stored.UID != r.UID {
		t.Fatal("account recovery changed the committed source identity", stored, err)
	}
	after := coverageView(t, tracker, cluster, gvr)
	if !after.ExtractionGapSince.Equal(before.ExtractionGapSince) || !after.ProcessingGapSince.Equal(prior) || !after.PersistenceGapSince.IsZero() {
		t.Fatal("valid account recovery erased known loss evidence", after)
	}
	account := (graph.Resource{ClusterID: cluster, Namespace: r.Namespace, Kind: "ServiceAccount", Name: "selected"}).ID()
	binding := (graph.Resource{ClusterID: cluster, Namespace: r.Namespace, Kind: "RoleBinding", Name: "selected-binding"}).ID()
	role := (graph.Resource{ClusterID: cluster, Namespace: r.Namespace, Kind: "Role", Name: "reader"}).ID()
	wantPath := []analysis.ImpactAuthorizationStep{
		{ImpactStep: analysis.ImpactStep{From: r.ID(), To: account, Type: graph.EdgeTypeUsesServiceAccount, TraversalFrom: r.ID(), TraversalTo: account}},
		{ImpactStep: analysis.ImpactStep{From: binding, To: account, Type: graph.EdgeTypeBindsSubject, TraversalFrom: account, TraversalTo: binding}, BindingScope: "namespace", BindingNamespace: r.Namespace},
		{ImpactStep: analysis.ImpactStep{From: binding, To: role, Type: graph.EdgeTypeBindsRole, TraversalFrom: binding, TraversalTo: role}, BindingScope: "namespace", BindingNamespace: r.Namespace},
	}
	for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependencies, analysis.ImpactDependents} {
		result, err := analysis.AnalyzeStoredImpact(ctx, base, r.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: relation}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil {
			t.Fatal(err)
		}
		auth := result.Authorization
		if auth.Counts.Total != 3 || auth.Counts.Direct != 1 || auth.Counts.Indirect != 2 || auth.Counts.ServiceAccounts != 1 || auth.Counts.Bindings != 1 || auth.Counts.Roles != 1 || auth.Counts.LowerBound || len(auth.Resources) != 3 || len(auth.Notices) != 0 || !auth.ModeledTraversalComplete || auth.EvidenceCategory != "stored_edge_association" || auth.EffectivePermissions != "not_evaluated" || auth.SourceVersionEvidence != "unknown" || auth.ObservationCoverage != "unknown" {
			t.Fatal("recovered account joined sibling/foreign grants or overstated evidence", auth)
		}
		for i, id := range []string{account, binding, role} {
			if auth.Resources[i].Resource.ID != id || !reflect.DeepEqual(auth.Resources[i].Path, wantPath[:i+1]) {
				t.Fatal("account association path changed scope, direction or binding metadata", auth)
			}
		}
		wantOrdinary := 0
		if relation == analysis.ImpactDependencies {
			wantOrdinary = 2
		}
		if result.Counts.Total != wantOrdinary {
			t.Fatal("authorization expansion changed ordinary counts", result)
		}
	}
}

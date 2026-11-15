// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
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

func TestCoreHTTPRouteParentGroupReplayAndImpact(t *testing.T) {
	runHTTPRouteReferenceGroupCases(t, false)
}

func TestCoreHTTPRouteServiceBackendGroupReplayAndImpact(t *testing.T) {
	runHTTPRouteReferenceGroupCases(t, true)
}

func runHTTPRouteReferenceGroupCases(t *testing.T, backend bool) {
	t.Helper()
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, state := range []string{"core-group", "foreign-group", "malformed-group", "write-failure", "missing-target"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, history, state), func(t *testing.T) {
					testHTTPRouteReferenceGroup(t, cluster, history, state, backend)
				})
			}
		}
	}
}

func testHTTPRouteReferenceGroup(t *testing.T, cluster string, history bool, state string, backend bool) {
	t.Helper()
	ctx := context.Background()
	base := memory.New()
	store, sink := &resyncStore{GraphStore: base}, &resyncSink{}
	tracker := operations.NewCoverageTracker(time.Minute)
	gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
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
	obj := resyncObject("HTTPRoute", "route", "route-uid", "version-one")
	obj.SetAPIVersion(gvr.GroupVersion().String())
	canonicalGroup, targetKind, targetVersion := "gateway.networking.k8s.io", "Gateway", "gateway.networking.k8s.io/v1"
	otherName, otherKind, otherNamespace, otherVersion := "backend", "Service", obj.GetNamespace(), "v1"
	edgeType := graph.EdgeTypeAttachedTo
	if backend {
		canonicalGroup, targetKind, targetVersion = "", "Service", "v1"
		otherName, otherKind, otherNamespace, otherVersion = "gateway", "Gateway", "infra", "gateway.networking.k8s.io/v1"
		edgeType = graph.EdgeTypeRoutesTo
	}
	reference := map[string]any{"name": "selected", "namespace": "infra", "group": canonicalGroup}
	batchSize := 4 // Three references plus an independent relationship.
	switch state {
	case "core-group":
		reference["group"], batchSize = "", 3
		if backend {
			reference["group"] = "core" // A literal group name is not the empty core group.
		}
	case "foreign-group":
		reference["group"], batchSize = "example.org", 3
	case "malformed-group":
		reference["group"], batchSize = map[string]any{"private": "reference-group-canary"}, 2
	}
	obj.Object["spec"] = map[string]any{
		"parentRefs": []any{map[string]any{"name": "confirmed", "namespace": "infra"}, reference, map[string]any{"name": "later", "namespace": "infra"}},
		"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"name": "backend"}}}},
	}
	if backend {
		obj.Object["spec"] = map[string]any{
			"parentRefs": []any{map[string]any{"name": otherName, "namespace": otherNamespace}},
			"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"name": "confirmed", "namespace": "infra"}, reference, map[string]any{"name": "later", "namespace": "infra"}}}},
		}
	}
	r := UnstructuredToResource(obj, "HTTPRoute")
	r.ClusterID = cluster
	selected := graph.Resource{ClusterID: cluster, Kind: targetKind, Namespace: "infra", Name: "selected", GroupVersion: targetVersion, UID: "target-uid", ResourceVersion: "target-version"}
	for _, name := range []string{"confirmed", "selected", "later", otherName} {
		row := selected
		row.Name = name
		if name == otherName {
			row.Kind, row.Namespace, row.GroupVersion = otherKind, otherNamespace, otherVersion
		}
		for _, owner := range []string{"east", "west", ""} {
			if state == "missing-target" && owner == cluster && name == "selected" {
				continue
			}
			copy := row
			copy.ClusterID = owner
			if err := base.UpsertResource(ctx, copy); err != nil {
				t.Fatal(err)
			}
		}
	}
	store.failEdge.Store(state == "write-failure")
	for _, event := range []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate} {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), event)
	}
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	wantEdges, wantNotifications := batchSize, int64(2)
	if state == "write-failure" {
		wantEdges, wantNotifications = 0, 1
	}
	if err != nil || len(edges) != wantEdges || store.resourceWrites.Load() != 1 || store.edgeWrites.Load() != int64(2*batchSize) || len(sink.recorded()) != 1 || notifications.Load() != wantNotifications {
		t.Fatal("reference extraction/replay lost its receipt, prefix or independent relationship", edges, sink.recorded(), err)
	}
	if state == "core-group" || state == "foreign-group" || state == "malformed-group" {
		for _, edge := range edges {
			if edge.To == selected.ID() {
				t.Fatal("unqualified reference group bound to a same-name target", edge)
			}
		}
	}
	before := coverageView(t, tracker, cluster, gvr)
	if before.ExtractionGapSince.IsZero() != (state != "malformed-group") || before.PersistenceGapSince.IsZero() != (state != "write-failure") || !before.ProcessingGapSince.Equal(prior) {
		t.Fatal("reference extraction changed gap classification", before)
	}
	if state == "missing-target" {
		result, err := analysis.AnalyzeStoredImpact(ctx, base, r.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependencies}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil || result.Counts.Total != 3 || !result.Counts.LowerBound || len(result.Notices) != 1 || result.Notices[0].Reason != analysis.ImpactMissingResource || result.Notices[0].Count != 1 {
			t.Fatal("foreign same-name target concealed an unresolved reference", result, err)
		}
		if _, err := base.GetResource(ctx, selected.ID()); !errors.As(err, new(graph.ErrNotFound)) {
			t.Fatal("declaration synthesized a missing target", err)
		}
		if err := base.UpsertResource(ctx, selected); err != nil {
			t.Fatal(err)
		}
	}
	wantResources := int64(1)
	if state == "core-group" || state == "foreign-group" || state == "malformed-group" {
		reference["group"] = canonicalGroup
		obj.SetResourceVersion("version-two")
		wantResources = 2
	}
	store.failEdge.Store(false)
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 4 || store.resourceWrites.Load() != wantResources || store.edgeWrites.Load() != int64(2*batchSize+8) || len(sink.recorded()) != int(wantResources) || notifications.Load() != wantNotifications+2 {
		t.Fatal("valid reference recovery rewrote unchanged history or lost edges", edges, sink.recorded(), err)
	}
	stored, err := base.GetResource(ctx, r.ID())
	if err != nil || stored.ResourceVersion != obj.GetResourceVersion() || stored.UID != r.UID {
		t.Fatal("reference recovery changed the committed source identity", stored, err)
	}
	after := coverageView(t, tracker, cluster, gvr)
	if !after.ExtractionGapSince.Equal(before.ExtractionGapSince) || !after.PersistenceGapSince.Equal(before.PersistenceGapSince) || !after.ProcessingGapSince.Equal(prior) {
		t.Fatal("healthy reference replay cleared prior failure evidence", after)
	}
	for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependencies, analysis.ImpactDependents} {
		root, target, count := r.ID(), selected.ID(), 4
		if relation == analysis.ImpactDependents {
			root, target, count = target, root, 1
		}
		result, err := analysis.AnalyzeStoredImpact(ctx, base, root, analysis.ImpactOptions{ClusterID: cluster, Relation: relation}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil || result.Counts.Total != count || result.Counts.Direct != count || len(result.Resources) != count || result.Counts.LowerBound {
			t.Fatal("qualified reference did not produce the expected scoped impact", result, err)
		}
		found := false
		for _, item := range result.Resources {
			if item.Resource.ID != target {
				continue
			}
			found = true
			if len(item.Path) != 1 {
				t.Fatal("reference path acquired an unexpected hop", item)
			}
			step := item.Path[0]
			if step.From != r.ID() || step.To != selected.ID() || step.Type != edgeType || step.TraversalFrom != root || step.TraversalTo != target {
				t.Fatal("reference path changed edge or traversal identity", step)
			}
		}
		if !found {
			t.Fatal("qualified reference missing from impact paths", result)
		}
	}
}

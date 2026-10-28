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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCoreScalesReferenceRecoveryAndQualification(t *testing.T) {
	for _, kind := range []string{"Deployment", "ReplicationController", "Widget"} {
		for _, cluster := range []string{"east", ""} {
			for _, history := range []bool{false, true} {
				for _, state := range []string{"spec-shape", "reference-shape", "identity-type", "version-type", "version-parse", "write-failure", "missing-target"} {
					t.Run(fmt.Sprintf("%s/cluster=%s/history=%t/%s", kind, cluster, history, state), func(t *testing.T) {
						testScalesReferenceRecovery(t, kind, cluster, history, state)
					})
				}
			}
		}
	}
}

func testScalesReferenceRecovery(t *testing.T, kind, cluster string, history bool, state string) {
	t.Helper()
	ctx := context.Background()
	base := memory.New()
	store, sink := &resyncStore{GraphStore: base}, &resyncSink{}
	tracker := operations.NewCoverageTracker(time.Minute)
	gvr := schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}
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
	version := "apps/v1"
	switch kind {
	case "ReplicationController":
		version = "v1"
	case "Widget":
		version = "example.org/v1"
	}
	obj := resyncObject("HorizontalPodAutoscaler", "autoscaler", "hpa-uid", "version-one")
	obj.SetAPIVersion(gvr.GroupVersion().String())
	obj.SetOwnerReferences([]metav1.OwnerReference{{Kind: "Deployment", Name: "manager", UID: "owner-uid", APIVersion: "apps/v1"}})
	ref := map[string]any{"kind": kind, "name": "selected", "apiVersion": version}
	obj.Object["spec"] = map[string]any{"scaleTargetRef": ref}
	switch state {
	case "spec-shape":
		obj.Object["spec"] = "shape-canary"
	case "reference-shape":
		obj.Object["spec"] = map[string]any{"scaleTargetRef": []any{ref}}
	case "identity-type":
		ref["name"] = true
	case "version-type":
		ref["apiVersion"] = map[string]any{"private": "version-canary"}
	case "version-parse":
		ref["apiVersion"] = "version-canary/v1/extra"
	}
	r := UnstructuredToResource(obj, "HorizontalPodAutoscaler")
	r.ClusterID = cluster
	selected := graph.Resource{ClusterID: cluster, Kind: kind, Namespace: r.Namespace, Name: "selected", GroupVersion: version, UID: "target-uid", ResourceVersion: "target-version"}
	owner := graph.Resource{ClusterID: cluster, Kind: "Deployment", Namespace: r.Namespace, Name: "manager", GroupVersion: "apps/v1", UID: "owner-uid", ResourceVersion: "owner-version"}
	for _, row := range []graph.Resource{selected, owner} {
		for _, scope := range []string{"east", "west", ""} {
			if state == "missing-target" && row.ID() == selected.ID() && scope == cluster {
				continue
			}
			copy := row
			copy.ClusterID = scope
			if err := base.UpsertResource(ctx, copy); err != nil {
				t.Fatal(err)
			}
		}
	}
	invalid := state != "write-failure" && state != "missing-target"
	batchSize := 2
	if invalid {
		batchSize = 1
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
		t.Fatal("HPA replay lost independent owner/source receipts or invented a scale edge", edges, sink.recorded(), err)
	}
	if invalid && (edges[0].From != r.ID() || edges[0].To != owner.ID() || edges[0].Type != graph.EdgeTypeOwns) {
		t.Fatal("malformed HPA reference suppressed independent ownership", edges)
	}
	before := coverageView(t, tracker, cluster, gvr)
	if before.ExtractionGapSince.IsZero() != !invalid || before.PersistenceGapSince.IsZero() != (state != "write-failure") || !before.ProcessingGapSince.Equal(prior) {
		t.Fatal("HPA reference failure lost distinct gap evidence", before)
	}
	if state == "missing-target" {
		if _, err := base.GetResource(ctx, selected.ID()); !errors.As(err, new(graph.ErrNotFound)) {
			t.Fatal("HPA declaration synthesized its unobserved target", err)
		}
		if err := base.UpsertResource(ctx, selected); err != nil {
			t.Fatal(err)
		}
	}
	wantSources := int64(1)
	if invalid {
		ref["kind"], ref["name"], ref["apiVersion"] = kind, "selected", version
		obj.Object["spec"] = map[string]any{"scaleTargetRef": ref}
		obj.SetResourceVersion("version-two")
		wantSources = 2
	}
	store.failEdge.Store(false)
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 2 || store.resourceWrites.Load() != wantSources || store.edgeWrites.Load() != int64(2*batchSize+4) || len(sink.recorded()) != int(wantSources) || notifications.Load() != wantNotifications+2 {
		t.Fatal("HPA recovery lost edges or duplicated unchanged source history", edges, sink.recorded(), err)
	}
	foundScale := false
	for _, edge := range edges {
		if edge.Type == graph.EdgeTypeScales {
			foundScale = edge.From == r.ID() && edge.To == selected.ID()
		}
	}
	if !foundScale {
		t.Fatal("HPA recovery changed target identity or member/namespace scope", edges)
	}
	stored, err := base.GetResource(ctx, r.ID())
	if err != nil || stored.UID != r.UID || stored.ResourceVersion != obj.GetResourceVersion() {
		t.Fatal("HPA recovery lost its committed identity", stored, err)
	}
	after := coverageView(t, tracker, cluster, gvr)
	if !after.ExtractionGapSince.Equal(before.ExtractionGapSince) || !after.PersistenceGapSince.Equal(before.PersistenceGapSince) || !after.ProcessingGapSince.Equal(prior) {
		t.Fatal("HPA recovery cleared known loss evidence", after)
	}
	for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependencies, analysis.ImpactDependents} {
		root, count := r.ID(), 1
		if relation == analysis.ImpactDependents {
			root, count = selected.ID(), 0
		}
		result, err := analysis.AnalyzeStoredImpact(ctx, base, root, analysis.ImpactOptions{ClusterID: cluster, Relation: relation}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil || result.Counts.Total != count || result.Counts.Direct != count || len(result.Resources) != count || result.Counts.LowerBound || !result.ModeledTraversalComplete || len(result.Notices) != 1 || result.Notices[0].Reason != analysis.ImpactUnmodeledRelationship || result.Notices[0].Count != 1 {
			t.Fatal("HPA recovery overstated unmodeled scale propagation or changed scoped counts", result, err)
		}
		if count == 1 {
			path := result.Resources[0].Path
			if result.Resources[0].Resource.ID != owner.ID() || len(path) != 1 || path[0].From != r.ID() || path[0].To != owner.ID() || path[0].Type != graph.EdgeTypeOwns || path[0].TraversalFrom != r.ID() || path[0].TraversalTo != owner.ID() {
				t.Fatal("independent owner path changed identity or traversal direction", result)
			}
		}
	}
}

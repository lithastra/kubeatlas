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

func TestCoreIngressDefaultBackendReplayAndImpact(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, state := range []string{"ready", "write-failure", "missing-target"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, history, state), func(t *testing.T) {
					testIngressDefaultReplay(t, cluster, history, state)
				})
			}
		}
	}
}

func testIngressDefaultReplay(t *testing.T, cluster string, history bool, state string) {
	t.Helper()
	failure := state == "write-failure"
	ctx := context.Background()
	base := memory.New()
	store := &resyncStore{GraphStore: base}
	sink := &resyncSink{}
	tracker := operations.NewCoverageTracker(time.Minute)
	gvr := schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	token, err := tracker.Begin(cluster, []schema.GroupVersionResource{gvr}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token.InitialDeliveryComplete(gvr)
	token.ListResult(gvr, true, false, time.Now())
	token.WatchStarted(gvr)
	prior := time.Now().Add(-time.Minute)
	token.RecordGap(gvr, operations.CoverageExtractionFailed, prior)
	var notifications atomic.Int64
	m := NewInformerManager(nil, store, WithClusterID(cluster), WithExtractor(extractor.New()), WithSnapshotSink(sink), WithMemberRepair(NewMemberRepair(nil, store, cluster)), WithBroadcaster(func(_, _, _, _ string) { notifications.Add(1) }))
	m.coverageSession = token
	if history {
		cancellationHistory(m, store, cluster, sink)
	}
	obj := resyncObject("Ingress", "entry", "ingress-uid", "opaque-version")
	obj.SetAPIVersion(gvr.GroupVersion().String())
	obj.Object["spec"] = map[string]any{"defaultBackend": map[string]any{"service": map[string]any{"name": "fallback", "port": map[string]any{"number": int64(80)}}}}
	r := UnstructuredToResource(obj, "Ingress")
	r.ClusterID = cluster
	service := graph.Resource{ClusterID: cluster, Namespace: r.Namespace, Kind: "Service", Name: "fallback", GroupVersion: "v1", UID: "service-uid", ResourceVersion: "service-version"}
	for _, owner := range []string{"east", "west", ""} {
		if state == "missing-target" && owner == cluster {
			continue
		}
		row := service
		row.ClusterID = owner
		if err := base.UpsertResource(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	// Model a resource already stored before this extractor gained default
	// backend support. Its same-version replay must still derive the new edge.
	m.handleUpsert(ctx, gvr, obj, graph.EventTypeAdd)
	if store.resourceWrites.Load() != 1 || store.edgeWrites.Load() != 0 || len(sink.recorded()) != 1 || notifications.Load() != 1 {
		t.Fatal("fixture did not commit exactly one resource receipt")
	}
	m.extractor = extractor.Default()
	store.failEdge.Store(failure)
	m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	wantEdges, wantNotifications := 1, int64(2)
	if failure {
		wantEdges, wantNotifications = 0, 1
	}
	if err != nil || len(edges) != wantEdges || store.edgeWrites.Load() != 1 || notifications.Load() != wantNotifications {
		t.Fatal("default backend replay skipped extraction or notified a failed write", edges, store.edgeWrites.Load(), notifications.Load(), err)
	}
	before := coverageView(t, tracker, cluster, gvr)
	if !before.ExtractionGapSince.Equal(prior) || before.PersistenceGapSince.IsZero() == failure || !before.ProcessingGapSince.IsZero() {
		t.Fatal("default backend delivery changed gap classification", before)
	}
	store.failEdge.Store(false)
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 1 || edges[0].From != r.ID() || edges[0].To != service.ID() || edges[0].Type != graph.EdgeTypeRoutesTo || store.resourceWrites.Load() != 1 || store.edgeWrites.Load() != 3 || len(sink.recorded()) != 1 || notifications.Load() != wantNotifications+2 {
		t.Fatal("healthy replay failed to repair/deduplicate the edge or manufactured resource history", edges, sink.recorded(), err)
	}
	after := coverageView(t, tracker, cluster, gvr)
	if !after.ExtractionGapSince.Equal(prior) || !after.PersistenceGapSince.Equal(before.PersistenceGapSince) || !after.ProcessingGapSince.IsZero() {
		t.Fatal("edge recovery cleared known loss evidence", after)
	}
	stored, err := base.GetResource(ctx, r.ID())
	if err != nil || !graph.SameResourceVersion(stored, r) {
		t.Fatal("edge backfill rewrote the stored resource receipt", stored, err)
	}
	if state == "missing-target" {
		result, err := analysis.AnalyzeStoredImpact(ctx, base, r.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependencies}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil || result.Counts.Total != 0 || !result.Counts.LowerBound || len(result.Notices) != 1 || result.Notices[0].Reason != analysis.ImpactMissingResource || result.Notices[0].Count != 1 {
			t.Fatal("foreign same-name Service concealed an unresolved backend", result, err)
		}
		if _, err := base.GetResource(ctx, service.ID()); !errors.As(err, new(graph.ErrNotFound)) {
			t.Fatal("backend declaration synthesized a Service", err)
		}
		if err := base.UpsertResource(ctx, service); err != nil {
			t.Fatal(err)
		}
	}
	for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependencies, analysis.ImpactDependents} {
		root, target := r.ID(), service.ID()
		if relation == analysis.ImpactDependents {
			root, target = target, root
		}
		result, err := analysis.AnalyzeStoredImpact(ctx, base, root, analysis.ImpactOptions{ClusterID: cluster, Relation: relation}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil || result.Counts.Total != 1 || result.Counts.Direct != 1 || len(result.Resources) != 1 || result.Resources[0].Resource.ID != target || len(result.Resources[0].Path) != 1 || len(result.ReferenceEvidence) != 0 {
			t.Fatal("default backend missing from scoped impact path", result, err)
		}
		step := result.Resources[0].Path[0]
		if step.From != r.ID() || step.To != service.ID() || step.Type != graph.EdgeTypeRoutesTo || step.TraversalFrom != root || step.TraversalTo != target {
			t.Fatal("impact changed routing edge or traversal direction", step)
		}
	}
}

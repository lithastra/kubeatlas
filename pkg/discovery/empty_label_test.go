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

type emptyLabelReadStore struct {
	*memory.Store
	fail bool
}

func (s *emptyLabelReadStore) ListResourcesInCluster(ctx context.Context, cluster string, f graph.Filter) ([]graph.Resource, error) {
	if s.fail {
		return nil, errors.New("selector read unavailable")
	}
	return s.Store.ListResourcesInCluster(ctx, cluster, f)
}

func TestCoreEmptyLabelSelectionReplayAndImpact(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, failure := range []bool{false, true} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/read-failure=%t", cluster, history, failure), func(t *testing.T) {
					testCoreEmptyLabelSelection(t, cluster, history, failure)
				})
			}
		}
	}
}

func testCoreEmptyLabelSelection(t *testing.T, cluster string, history, failure bool) {
	t.Helper()
	ctx := context.Background()
	base := memory.New()
	reads := &emptyLabelReadStore{Store: base, fail: failure}
	store := &resyncStore{GraphStore: reads}
	sink := &resyncSink{}
	tracker := operations.NewCoverageTracker(time.Minute)
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "services"}
	token, err := tracker.Begin(cluster, []schema.GroupVersionResource{gvr}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token.InitialDeliveryComplete(gvr)
	token.ListResult(gvr, true, false, time.Now())
	token.WatchStarted(gvr)
	prior := time.Now().Add(-time.Minute)
	token.RecordGap(gvr, operations.CoveragePersistenceFailed, prior)
	registry := extractor.New()
	registry.Register(extractor.SelectsExtractor{})
	var notifications atomic.Int64
	m := NewInformerManager(nil, store, WithClusterID(cluster), WithExtractor(registry), WithSnapshotSink(sink), WithMemberRepair(NewMemberRepair(nil, store, cluster)), WithBroadcaster(func(_, _, _, _ string) { notifications.Add(1) }))
	m.coverageSession = token
	if history {
		cancellationHistory(m, store, cluster, sink)
	}
	obj := resyncObject("Service", "selector", "service-uid", "service-version")
	obj.Object["spec"] = map[string]any{"selector": map[string]any{"flag": ""}}
	r := UnstructuredToResource(obj, "Service")
	r.ClusterID = cluster
	selected := graph.Resource{ClusterID: cluster, Namespace: r.Namespace, Kind: "Pod", Name: "selected", GroupVersion: "v1", UID: "pod-uid", ResourceVersion: "pod-version", Labels: map[string]string{"flag": ""}}
	missing := selected
	missing.Name, missing.UID, missing.Labels = "missing", "missing-uid", map[string]string{"other": ""}
	for _, row := range []graph.Resource{selected, missing} {
		for _, owner := range []string{"east", "west", ""} {
			copy := row
			copy.ClusterID = owner
			if err := base.UpsertResource(ctx, copy); err != nil {
				t.Fatal(err)
			}
		}
	}
	otherNamespace := selected
	otherNamespace.Namespace = "other"
	if err := base.UpsertResource(ctx, otherNamespace); err != nil {
		t.Fatal(err)
	}
	m.handleUpsert(ctx, gvr, obj, graph.EventTypeAdd)
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	wantEdges := 1
	if failure {
		wantEdges = 0
	}
	if err != nil || len(edges) != wantEdges || store.edgeWrites.Load() != int64(wantEdges) || store.resourceWrites.Load() != 1 || len(sink.recorded()) != 1 || notifications.Load() != 1 {
		t.Fatal("selector delivery committed extra edges or lost its resource receipt", edges, sink.recorded(), err)
	}
	before := coverageView(t, tracker, cluster, gvr)
	if before.ExtractionGapSince.IsZero() == failure || !before.PersistenceGapSince.Equal(prior) || !before.ProcessingGapSince.IsZero() {
		t.Fatal("selector read failure lost its gap classification", before)
	}
	reads.fail = false
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 1 || edges[0].From != r.ID() || edges[0].To != selected.ID() || edges[0].Type != graph.EdgeTypeSelects || store.resourceWrites.Load() != 1 || store.edgeWrites.Load() != int64(wantEdges+2) || len(sink.recorded()) != 1 || notifications.Load() != 3 {
		t.Fatal("replay matched an absent key, rewrote history or escaped scope", edges, sink.recorded(), err)
	}
	after := coverageView(t, tracker, cluster, gvr)
	if !after.ExtractionGapSince.Equal(before.ExtractionGapSince) || !after.PersistenceGapSince.Equal(prior) || !after.ProcessingGapSince.IsZero() {
		t.Fatal("healthy selector replay cleared known loss evidence", after)
	}
	for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependencies, analysis.ImpactDependents} {
		root, target := r.ID(), selected.ID()
		if relation == analysis.ImpactDependents {
			root, target = target, root
		}
		result, err := analysis.AnalyzeStoredImpact(ctx, base, root, analysis.ImpactOptions{ClusterID: cluster, Relation: relation}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil || result.Counts.Total != 1 || result.Counts.Direct != 1 || len(result.Resources) != 1 || result.Resources[0].Resource.ID != target || len(result.Resources[0].Path) != 1 {
			t.Fatal("label equality produced an incorrect scoped impact path", result, err)
		}
		step := result.Resources[0].Path[0]
		if step.From != r.ID() || step.To != selected.ID() || step.Type != graph.EdgeTypeSelects || step.TraversalFrom != root || step.TraversalTo != target {
			t.Fatal("selector impact changed edge or traversal identity", step)
		}
	}
	result, err := analysis.AnalyzeStoredImpact(ctx, base, missing.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependents}, graph.ImpactSnapshotOptions{ClusterID: cluster})
	if err != nil || result.Counts.Total != 0 || len(result.Resources) != 0 || result.Counts.LowerBound {
		t.Fatal("absent label key acquired a Service dependent", result, err)
	}
}

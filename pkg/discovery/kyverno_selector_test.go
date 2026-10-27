// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

func TestCoreKyvernoMalformedSelectorRecovery(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, prefix := range []bool{false, true} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/prefix=%t", cluster, history, prefix), func(t *testing.T) {
					testCoreKyvernoSelector(t, cluster, history, prefix)
				})
			}
		}
	}
}

func testCoreKyvernoSelector(t *testing.T, cluster string, history, prefix bool) {
	t.Helper()
	ctx := context.Background()
	m, base, store, sink, tracker, obj := kyvernoCoverageFixture(t, cluster, history)
	m.snapshotSink = sink
	prior := time.Now().Add(-time.Minute)
	m.coverageSession.RecordGap(kyvernoCoverageGVR, operations.CoverageProcessingFailed, prior)
	badResources := map[string]any{"kinds": []any{"Deployment"}, "namespaces": []any{"test"}, "selector": map[string]any{"matchLabels": map[string]any{"app": "selected", "flag": "", "invalid": true}}}
	descriptors := []any{map[string]any{"resources": badResources}, map[string]any{"resources": map[string]any{"kinds": []any{"Service"}}}}
	if prefix {
		descriptors = append([]any{map[string]any{"resources": map[string]any{"kinds": []any{"Pod"}, "namespaces": []any{"test"}, "selector": map[string]any{"matchLabels": map[string]any{"app": "selected"}}}}}, descriptors...)
	}
	obj.Object["spec"] = map[string]any{"rules": []any{map[string]any{"validate": map[string]any{}, "match": map[string]any{"any": descriptors}}}}
	r := UnstructuredToResource(obj, "ClusterPolicy")
	r.ClusterID = cluster
	selected := graph.Resource{ClusterID: cluster, Kind: "Deployment", Namespace: "test", Name: "selected", Labels: map[string]string{"app": "selected", "flag": ""}}
	for _, owner := range []string{"east", "west", ""} {
		row := selected
		row.ClusterID = owner
		if err := base.UpsertResource(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	decoy := selected
	decoy.Name, decoy.Labels = "missing-key", map[string]string{"app": "selected"}
	if err := base.UpsertResource(ctx, decoy); err != nil {
		t.Fatal(err)
	}
	var reads []string
	store.read = func(ctx context.Context, owner string, f graph.Filter) ([]graph.Resource, error) {
		if owner != cluster || f.ClusterID != cluster {
			t.Fatal("policy selector query escaped its scope", owner, f)
		}
		reads = append(reads, f.Kind)
		return base.ListResourcesInCluster(ctx, owner, f)
	}
	for _, event := range []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate} {
		reads = nil
		m.handleUpsert(ctx, kyvernoCoverageGVR, obj.DeepCopy(), event)
		want := []string{"PolicyReport", "ClusterPolicyReport"}
		if prefix {
			want = append(want, "Pod")
		}
		if !reflect.DeepEqual(reads, want) {
			t.Fatal("invalid descriptor queried targets or continued later descriptors", reads)
		}
	}
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	wantInitial := 0
	if prefix {
		wantInitial = 1
	}
	if err != nil || len(edges) != wantInitial || len(sink.recorded()) != 1 {
		t.Fatal("invalid policy replay lost prefix or duplicated source history", edges, sink.recorded(), err)
	}
	if prefix && (edges[0].To != (graph.Resource{ClusterID: cluster, Kind: "Pod", Namespace: "test", Name: "same"}).ID() || edges[0].Attributes["result"] != "pass") {
		t.Fatal("malformed later descriptor changed the confirmed report overlay", edges)
	}
	stored, err := base.GetResource(ctx, r.ID())
	if err != nil || !graph.SameResourceVersion(stored, r) {
		t.Fatal("malformed selector discarded its committed source receipt", stored, err)
	}
	before := coverageView(t, tracker, cluster, kyvernoCoverageGVR)
	if before.ExtractionGapSince.IsZero() || !before.ProcessingGapSince.Equal(prior) || !before.PersistenceGapSince.IsZero() {
		t.Fatal("policy selector failure lost distinct gap evidence", before)
	}
	obj.SetResourceVersion("valid-version")
	badResources["selector"] = map[string]any{"matchLabels": map[string]any{"app": "selected", "flag": ""}}
	for range 2 {
		m.handleUpsert(ctx, kyvernoCoverageGVR, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != wantInitial+1 || len(sink.recorded()) != 2 || sink.recorded()[1].ResourceVersion != "valid-version" {
		t.Fatal("valid source/replay failed to recover scoped edges without duplicate history", edges, sink.recorded(), err)
	}
	for _, e := range edges {
		podID := (graph.Resource{ClusterID: cluster, Kind: "Pod", Namespace: "test", Name: "same"}).ID()
		if e.From != r.ID() || e.Type != graph.EdgeTypeEnforces || (e.To != selected.ID() && (!prefix || e.To != podID)) {
			t.Fatal("recovered policy selected an absent label key or foreign target", e)
		}
	}
	after := coverageView(t, tracker, cluster, kyvernoCoverageGVR)
	if !after.ExtractionGapSince.Equal(before.ExtractionGapSince) || !after.ProcessingGapSince.Equal(prior) || !after.PersistenceGapSince.IsZero() {
		t.Fatal("policy recovery cleared prior failure evidence", after)
	}
	result, err := analysis.AnalyzeStoredImpact(ctx, base, r.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependencies}, graph.ImpactSnapshotOptions{ClusterID: cluster})
	if err != nil || result.Counts.Total != 0 || len(result.Notices) != 1 || result.Notices[0].Reason != analysis.ImpactUnmodeledRelationship || result.Notices[0].Count != wantInitial+1 {
		t.Fatal("policy edges entered ordinary impact or lost their exclusion notice", result, err)
	}
}

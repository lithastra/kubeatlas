// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package gatekeeper

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestConstraintMalformedSelectorPreservesOriginAndRecovery(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			t.Run(fmt.Sprintf("cluster=%s/history=%t", cluster, history), func(t *testing.T) {
				testConstraintSelectorRecovery(t, cluster, history)
			})
		}
	}
}

func testConstraintSelectorRecovery(t *testing.T, cluster string, history bool) {
	t.Helper()
	ctx := context.Background()
	base := memory.New()
	store := &partialConstraintStore{Store: base, edge: base.UpsertEdge}
	reads := 0
	store.read = func(ctx context.Context, owner string, f graph.Filter) ([]graph.Resource, error) {
		reads++
		if owner != cluster || f.ClusterID != cluster || f.Kind != "Pod" || f.Namespace != "default" {
			t.Fatal("constraint selector query escaped its scope", owner, f)
		}
		return base.ListResourcesInCluster(ctx, owner, f)
	}
	tracker := operations.NewCoverageTracker(time.Minute)
	source, err := tracker.BeginSource(cluster, operations.CoverageSourceGatekeeper, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.BeginType(coverageConstraintGVR, time.Now())
	if err != nil || !token.BindOrigin(graph.ResourceOrigin{OwnerName: "k8srequiredlabels", OwnerUID: "template-uid", Group: constraintGroup, Version: constraintVersion, Resource: coverageConstraintGVR.Resource, Kind: "K8sRequiredLabels"}) {
		t.Fatal("source receipt binding failed", err)
	}
	prior := time.Now().Add(-time.Minute)
	token.RecordGap(coverageConstraintGVR, operations.CoverageProcessingFailed, prior)
	_, obj := coverageObjects()
	match := map[string]any{"kinds": []any{map[string]any{"kinds": []any{"Pod"}}}, "namespaces": []any{"default"}, "labelSelector": map[string]any{"matchLabels": map[string]any{"app": "web", "flag": "", "invalid": true}}}
	obj.Object["spec"] = map[string]any{"match": match}
	obj.Object["status"] = map[string]any{"violations": []any{map[string]any{"kind": "Pod", "namespace": "default", "name": "selected", "message": "observed violation"}}}
	r := discovery.UnstructuredToResource(obj, obj.GetKind())
	r.ClusterID = cluster
	r = token.StampOrigin(r)
	selected := graph.Resource{ClusterID: cluster, Kind: "Pod", Namespace: "default", Name: "selected", Labels: map[string]string{"app": "web", "flag": ""}}
	for _, owner := range []string{"east", "west", ""} {
		row := selected
		row.ClusterID = owner
		if err := base.UpsertResource(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	decoy := selected
	decoy.Name, decoy.Labels = "missing-key", map[string]string{"app": "web"}
	if err := base.UpsertResource(ctx, decoy); err != nil {
		t.Fatal(err)
	}
	d := New(nil, store, extractor.Default(), nil, WithClusterID(cluster), WithMemberRepair(discovery.NewMemberRepair(nil, store, cluster)))
	sink := &dynamicHistorySink{}
	if history {
		d.resourceHistory = discovery.NewResourceHistory(store, cluster, sink)
		if cluster == "" {
			d.resourceHistory = discovery.NewStandaloneResourceHistory(store, sink)
		}
	}
	for _, event := range []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate} {
		d.handleConstraintEvent(ctx, r.Kind, obj.DeepCopy(), token, event)
	}
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	stored, receiptErr := base.GetResource(ctx, r.ID())
	wantHistory := 0
	if history {
		wantHistory = 1
	}
	if err != nil || receiptErr != nil || reads != 0 || len(edges) != 0 || !stored.Origin.Matches(stored) || !graph.SameResourceVersion(stored, r) || len(sink.events) != wantHistory {
		t.Fatal("malformed constraint queried targets or lost its source/history receipt", reads, edges, stored, sink.events, err, receiptErr)
	}
	before := coverageRow(memberGKSource(t, tracker, cluster), coverageConstraintGVR)
	if before.ExtractionGapSince.IsZero() || !before.ProcessingGapSince.Equal(prior) || !before.PersistenceGapSince.IsZero() {
		t.Fatal("constraint selector failure changed gap classification", before)
	}
	obj.SetResourceVersion("valid-version")
	match["labelSelector"] = map[string]any{"matchLabels": map[string]any{"app": "web", "flag": ""}}
	for range 2 {
		d.handleConstraintEvent(ctx, r.Kind, obj.DeepCopy(), token, graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	retained, receiptErr := base.GetResource(ctx, r.ID())
	if history {
		wantHistory = 2
	}
	if err != nil || receiptErr != nil || reads != 2 || len(edges) != 1 || edges[0].From != r.ID() || edges[0].To != selected.ID() || edges[0].Type != graph.EdgeTypeEnforces || edges[0].Attributes["violated"] != "true" || edges[0].Attributes["violation_message"] != "observed violation" || len(sink.events) != wantHistory || !retained.Origin.Matches(retained) || retained.Origin.Registration != stored.Origin.Registration || retained.Origin.OwnerUID != stored.Origin.OwnerUID || retained.ResourceVersion != "valid-version" {
		t.Fatal("valid constraint/replay lost scoped matches, violation data or source ownership", edges, retained, sink.events, err, receiptErr)
	}
	after := coverageRow(memberGKSource(t, tracker, cluster), coverageConstraintGVR)
	if !after.ExtractionGapSince.Equal(before.ExtractionGapSince) || !after.ProcessingGapSince.Equal(prior) || !after.PersistenceGapSince.IsZero() {
		t.Fatal("constraint recovery cleared prior loss evidence", after)
	}
	result, err := analysis.AnalyzeStoredImpact(ctx, base, r.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependencies}, graph.ImpactSnapshotOptions{ClusterID: cluster})
	if err != nil || result.Counts.Total != 0 || len(result.Notices) != 1 || result.Notices[0].Reason != analysis.ImpactUnmodeledRelationship || result.Notices[0].Count != 1 {
		t.Fatal("constraint edge entered ordinary impact or lost its exclusion notice", result, err)
	}
}

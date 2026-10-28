// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCoreSelectorMetadataPathReplayAndImpact(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "CronJob"} {
		for _, service := range []bool{false, true} {
			for _, cluster := range []string{"east", ""} {
				for _, history := range []bool{false, true} {
					for _, state := range []string{"missing-metadata", "wrong-template", "masked-match", "write-failure"} {
						t.Run(fmt.Sprintf("%s/service=%t/cluster=%s/history=%t/%s", kind, service, cluster, history, state), func(t *testing.T) {
							testSelectorMetadataReplay(t, kind, service, cluster, history, state)
						})
					}
				}
			}
		}
	}
}

func testSelectorMetadataReplay(t *testing.T, kind string, service bool, cluster string, history bool, state string) {
	t.Helper()
	ctx := context.Background()
	base := memory.New()
	store, sink := &resyncStore{GraphStore: base}, &resyncSink{}
	gvr := schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}
	sourceKind, relationshipCount := "NetworkPolicy", 3
	if service {
		gvr.Group, gvr.Resource, sourceKind, relationshipCount = "", "services", "Service", 1
	}
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
	obj := resyncObject(sourceKind, "source", "source-uid", "source-version")
	obj.SetAPIVersion(gvr.GroupVersion().String())
	obj.SetOwnerReferences([]metav1.OwnerReference{{Kind: "ConfigMap", Name: "owner", UID: "owner-uid", APIVersion: "v1"}})
	labels := map[string]any{"app": "selected"}
	obj.Object["spec"] = map[string]any{
		"podSelector": map[string]any{"matchLabels": labels}, "policyTypes": []any{"Ingress", "Egress"},
		"ingress": []any{map[string]any{"from": []any{map[string]any{"podSelector": map[string]any{"matchLabels": labels}}}}},
		"egress":  []any{map[string]any{"to": []any{map[string]any{"podSelector": map[string]any{"matchLabels": labels}}}}},
	}
	if service {
		obj.Object["spec"] = map[string]any{"selector": labels}
	}
	r := UnstructuredToResource(obj, sourceKind)
	r.ClusterID = cluster
	targetObject := selectorMetadataTarget(kind, state)
	target := UnstructuredToResource(targetObject, kind)
	target.ClusterID = cluster
	owner := graph.Resource{ClusterID: cluster, Kind: "ConfigMap", Namespace: r.Namespace, Name: "owner", UID: "owner-uid", ResourceVersion: "owner-version", GroupVersion: "v1"}
	for _, scope := range []string{"east", "west", ""} {
		copy := UnstructuredToResource(selectorMetadataTarget(kind, "masked-match"), kind)
		copy.ClusterID = scope
		if scope == cluster {
			copy = target
		}
		if err := base.UpsertResource(ctx, copy); err != nil {
			t.Fatal(err)
		}
		copy = owner
		copy.ClusterID = scope
		if err := base.UpsertResource(ctx, copy); err != nil {
			t.Fatal(err)
		}
	}
	otherNamespace := UnstructuredToResource(selectorMetadataTarget(kind, "masked-match"), kind)
	otherNamespace.ClusterID, otherNamespace.Namespace = cluster, "other"
	if err := base.UpsertResource(ctx, otherNamespace); err != nil {
		t.Fatal(err)
	}
	initialBatch := 1
	if state == "masked-match" || state == "write-failure" {
		initialBatch += relationshipCount
	}
	store.failEdge.Store(state == "write-failure")
	for _, event := range []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate} {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), event)
	}
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	wantEdges, wantNotifications := initialBatch, int64(2)
	if state == "write-failure" {
		wantEdges, wantNotifications = 0, 1
	}
	if err != nil || len(edges) != wantEdges || store.resourceWrites.Load() != 1 || store.edgeWrites.Load() != int64(2*initialBatch) || len(sink.recorded()) != 1 || notifications.Load() != wantNotifications {
		t.Fatal("selector path replay used controller/alternate labels or lost owner/source receipts", edges, sink.recorded(), err)
	}
	if initialBatch == 1 && (edges[0].Type != graph.EdgeTypeOwns || edges[0].To != owner.ID()) {
		t.Fatal("nonmatching canonical labels suppressed independent ownership", edges)
	}
	before := coverageView(t, tracker, cluster, gvr)
	if !before.ExtractionGapSince.IsZero() || before.PersistenceGapSince.IsZero() != (state != "write-failure") || !before.ProcessingGapSince.Equal(prior) {
		t.Fatal("selector metadata lookup changed failure classification", before)
	}
	if initialBatch == 1 {
		// The candidate changes, not the Service/NetworkPolicy source. Replaying
		// the unchanged source must read current canonical candidate labels.
		replacement := selectorMetadataTarget(kind, "masked-match")
		replacement.SetResourceVersion("target-version-two")
		updated := UnstructuredToResource(replacement, kind)
		updated.ClusterID = cluster
		if err := base.UpsertResource(ctx, updated); err != nil {
			t.Fatal(err)
		}
	}
	store.failEdge.Store(false)
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 1+relationshipCount || store.resourceWrites.Load() != 1 || store.edgeWrites.Load() != int64(2*initialBatch+2*(1+relationshipCount)) || len(sink.recorded()) != 1 || notifications.Load() != wantNotifications+2 {
		t.Fatal("same-version selector replay lost relationships or duplicated source history", edges, sink.recorded(), err)
	}
	for _, edge := range edges {
		if edge.From != r.ID() || edge.Type == graph.EdgeTypeOwns && edge.To != owner.ID() || edge.Type != graph.EdgeTypeOwns && edge.To != target.ID() {
			t.Fatal("selector recovery joined a foreign member or namespace", edge)
		}
	}
	stored, err := base.GetResource(ctx, r.ID())
	if err != nil || stored.UID != r.UID || stored.ResourceVersion != obj.GetResourceVersion() {
		t.Fatal("candidate metadata update changed the source receipt", stored, err)
	}
	after := coverageView(t, tracker, cluster, gvr)
	if !after.ExtractionGapSince.IsZero() || !after.PersistenceGapSince.Equal(before.PersistenceGapSince) || !after.ProcessingGapSince.Equal(prior) {
		t.Fatal("selector recovery cleared retained loss evidence", after)
	}
	for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependencies, analysis.ImpactDependents} {
		root, count := r.ID(), 1
		if service {
			count = 2
		}
		if relation == analysis.ImpactDependents {
			root, count = target.ID(), 0
			if service {
				count = 1
			}
		}
		result, err := analysis.AnalyzeStoredImpact(ctx, base, root, analysis.ImpactOptions{ClusterID: cluster, Relation: relation}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil || result.Counts.Total != count || result.Counts.Direct != count || len(result.Resources) != count || result.Counts.LowerBound || !result.ModeledTraversalComplete {
			t.Fatal("selector path recovery changed scoped modeled counts", result, err)
		}
		if service && len(result.Notices) != 0 || !service && (len(result.Notices) != 1 || result.Notices[0].Reason != analysis.ImpactUnmodeledRelationship || result.Notices[0].Count != 3) {
			t.Fatal("NetworkPolicy declarations became modeled traffic propagation", result.Notices)
		}
		for _, item := range result.Resources {
			if item.Resource.ID != target.ID() && relation == analysis.ImpactDependencies || item.Resource.ID != r.ID() && relation == analysis.ImpactDependents {
				continue // The independent owner is another forward dependency.
			}
			path := item.Path
			if len(path) != 1 || path[0].From != r.ID() || path[0].To != target.ID() || path[0].Type != graph.EdgeTypeSelects || path[0].TraversalFrom != root || path[0].TraversalTo != item.Resource.ID {
				t.Fatal("Service template association changed stored/traversal direction", path)
			}
		}
	}
}

func selectorMetadataTarget(kind, state string) *unstructured.Unstructured {
	u := resyncObject(kind, "target", "target-uid", "target-version-one")
	version := "apps/v1"
	switch kind {
	case "Pod":
		version = "v1"
	case "CronJob":
		version = "batch/v1"
	}
	u.SetAPIVersion(version)
	match := state == "masked-match" || state == "write-failure"
	app, decoy := "other", "selected"
	if match {
		app, decoy = "selected", "other"
	}
	u.SetLabels(map[string]string{"app": decoy})
	template := map[string]any{"spec": map[string]any{}}
	if state != "missing-metadata" {
		template["metadata"] = map[string]any{"labels": map[string]any{"app": app}}
	}
	decoyTemplate := map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": decoy}}}
	switch kind {
	case "Pod":
		if state == "missing-metadata" {
			u.SetLabels(nil)
		} else {
			u.SetLabels(map[string]string{"app": app})
		}
		u.Object["spec"] = map[string]any{"template": decoyTemplate, "jobTemplate": map[string]any{"spec": map[string]any{"template": decoyTemplate}}}
	case "CronJob":
		u.Object["spec"] = map[string]any{"template": decoyTemplate, "jobTemplate": map[string]any{"spec": map[string]any{"template": template}}}
	default:
		u.Object["spec"] = map[string]any{"template": template}
	}
	return u
}

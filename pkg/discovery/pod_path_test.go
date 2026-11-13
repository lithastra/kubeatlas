// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
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

func TestCoreBuiltinPodSpecPathReplayAndImpact(t *testing.T) {
	for _, fixture := range []struct{ kind, group, resource string }{
		{"Pod", "", "pods"}, {"Deployment", "apps", "deployments"}, {"ReplicaSet", "apps", "replicasets"},
		{"StatefulSet", "apps", "statefulsets"}, {"DaemonSet", "apps", "daemonsets"}, {"Job", "batch", "jobs"}, {"CronJob", "batch", "cronjobs"},
	} {
		for _, cluster := range []string{"east", ""} {
			for _, history := range []bool{false, true} {
				for _, state := range []string{"empty-lists", "populated", "write-failure", "missing-target"} {
					t.Run(fmt.Sprintf("%s/cluster=%s/history=%t/%s", fixture.kind, cluster, history, state), func(t *testing.T) {
						gvr := schema.GroupVersionResource{Group: fixture.group, Version: "v1", Resource: fixture.resource}
						testPodPathReplay(t, fixture.kind, gvr, cluster, history, state)
					})
				}
			}
		}
	}
}

func testPodPathReplay(t *testing.T, kind string, gvr schema.GroupVersionResource, cluster string, history bool, state string) {
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
	spec := map[string]any{"serviceAccountName": "selected-account", "containers": []any{}, "initContainers": []any{}, "ephemeralContainers": []any{}, "volumes": []any{}, "imagePullSecrets": []any{}}
	populate := func() {
		spec["containers"] = []any{map[string]any{"name": "main", "envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "selected-config"}}}}}
		spec["volumes"] = []any{map[string]any{"name": "claim", "persistentVolumeClaim": map[string]any{"claimName": "selected-claim"}}}
		spec["imagePullSecrets"] = []any{map[string]any{"name": "selected-secret"}}
	}
	if state != "empty-lists" {
		populate()
	}
	outer := spec
	if kind == "CronJob" {
		outer = map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": spec}}}}
	} else if kind != "Pod" {
		outer = map[string]any{"template": map[string]any{"spec": spec}}
	}
	decoy := map[string]any{"serviceAccountName": "decoy-account", "imagePullSecrets": []any{map[string]any{"name": "decoy-secret"}}, "volumes": []any{
		map[string]any{"configMap": map[string]any{"name": "decoy-config"}},
		map[string]any{"secret": map[string]any{"secretName": "decoy-secret"}},
		map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "decoy-claim"}},
	}}
	for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
		decoy[field] = []any{map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "decoy-config"}}, map[string]any{"secretRef": map[string]any{"name": "decoy-secret"}}}}}
	}
	if kind != "Pod" {
		for field, value := range decoy {
			outer[field] = value
		}
	}
	if kind == "Pod" || kind == "CronJob" {
		outer["template"] = map[string]any{"spec": decoy}
	}
	if kind != "CronJob" {
		outer["jobTemplate"] = map[string]any{"spec": map[string]any{"template": map[string]any{"spec": decoy}}}
	}
	obj.Object["spec"] = outer
	r := UnstructuredToResource(obj, kind)
	r.ClusterID = cluster
	targets := map[graph.EdgeType]graph.Resource{}
	for edgeType, targetKind := range map[graph.EdgeType]string{graph.EdgeTypeUsesConfigMap: "ConfigMap", graph.EdgeTypeUsesSecret: "Secret", graph.EdgeTypeMountsVolume: "PersistentVolumeClaim", graph.EdgeTypeUsesServiceAccount: "ServiceAccount"} {
		name := map[graph.EdgeType]string{graph.EdgeTypeUsesConfigMap: "selected-config", graph.EdgeTypeUsesSecret: "selected-secret", graph.EdgeTypeMountsVolume: "selected-claim", graph.EdgeTypeUsesServiceAccount: "selected-account"}[edgeType]
		target := graph.Resource{ClusterID: cluster, Kind: targetKind, Namespace: r.Namespace, Name: name, GroupVersion: "v1", UID: "target-uid", ResourceVersion: "target-version"}
		targets[edgeType] = target
		for _, scope := range []string{"east", "west", ""} {
			if scope == cluster && (targetKind == "Secret" || state == "missing-target" && (targetKind == "ConfigMap" || targetKind == "PersistentVolumeClaim")) {
				continue
			}
			copy := target
			copy.ClusterID = scope
			if err := base.UpsertResource(ctx, copy); err != nil {
				t.Fatal(err)
			}
		}
	}
	store.failEdge.Store(state == "write-failure")
	for _, event := range []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate} {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), event)
	}
	if _, err := base.GetResource(ctx, graph.SecretReferenceResource(r.Namespace, "decoy-secret", cluster).ID()); !errors.As(err, new(graph.ErrNotFound)) {
		t.Fatal("noncanonical Pod-like fields materialized a Secret reference", err)
	}
	// Secret references have no observed UID/version, so each delivery writes
	// their reference-only receipt even when the source receipt is unchanged.
	initialBatch, initialWrites, wantNotifications := 4, int64(3), int64(2)
	if state == "empty-lists" {
		initialBatch, initialWrites = 1, 1
	}
	wantEdges := initialBatch
	if state == "write-failure" {
		wantEdges = 0
	}
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != wantEdges || store.resourceWrites.Load() != initialWrites || store.edgeWrites.Load() != int64(2*initialBatch) || len(sink.recorded()) != 1 || notifications.Load() != wantNotifications {
		t.Fatal("Pod path replay lost receipts or read references from a different path", edges, sink.recorded(), err)
	}
	before := coverageView(t, tracker, cluster, gvr)
	if !before.ExtractionGapSince.IsZero() || before.PersistenceGapSince.IsZero() != (state != "write-failure") || !before.ProcessingGapSince.Equal(prior) {
		t.Fatal("Pod path lookup changed gap classification", before)
	}
	if state == "missing-target" {
		initial, err := analysis.AnalyzeStoredImpact(ctx, base, r.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependencies}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil || initial.Counts.Total != 2 || !initial.Counts.LowerBound || len(initial.Notices) != 1 || initial.Notices[0].Reason != analysis.ImpactMissingResource || initial.Notices[0].Count != 2 {
			t.Fatal("foreign same-name targets concealed unobserved canonical references", initial, err)
		}
		for _, edgeType := range []graph.EdgeType{graph.EdgeTypeUsesConfigMap, graph.EdgeTypeMountsVolume} {
			target := targets[edgeType]
			if _, err := base.GetResource(ctx, target.ID()); !errors.As(err, new(graph.ErrNotFound)) {
				t.Fatal("Pod declaration synthesized an unobserved target", err)
			}
			if err := base.UpsertResource(ctx, target); err != nil {
				t.Fatal(err)
			}
		}
	}
	wantWrites, wantHistory := int64(5), 1
	if state == "empty-lists" {
		populate()
		obj.SetResourceVersion("version-two")
		wantWrites, wantHistory = 4, 2
	}
	store.failEdge.Store(false)
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 4 || store.resourceWrites.Load() != wantWrites || store.edgeWrites.Load() != int64(2*initialBatch+8) || len(sink.recorded()) != wantHistory || notifications.Load() != wantNotifications+2 {
		t.Fatal("canonical path recovery rewrote history or lost reference edges", edges, sink.recorded(), err)
	}
	for _, edge := range edges {
		if edge.From != r.ID() || edge.To != targets[edge.Type].ID() || strings.Contains(edge.To, "decoy") {
			t.Fatal("recovery retained a noncanonical Pod path association", edge)
		}
	}
	secret, err := base.GetResource(ctx, targets[graph.EdgeTypeUsesSecret].ID())
	if err != nil || !reflect.DeepEqual(secret, graph.SecretReferenceResource(r.Namespace, "selected-secret", cluster)) {
		t.Fatal("canonical Secret endpoint exceeded the reference-only boundary", secret, err)
	}
	stored, err := base.GetResource(ctx, r.ID())
	if err != nil || stored.UID != r.UID || stored.ResourceVersion != obj.GetResourceVersion() {
		t.Fatal("canonical path recovery changed source identity", stored, err)
	}
	after := coverageView(t, tracker, cluster, gvr)
	if !after.ProcessingGapSince.Equal(prior) || !after.PersistenceGapSince.Equal(before.PersistenceGapSince) || !after.ExtractionGapSince.IsZero() {
		t.Fatal("healthy path replay erased known loss evidence", after)
	}
	forward, err := analysis.AnalyzeStoredImpact(ctx, base, r.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependencies}, graph.ImpactSnapshotOptions{ClusterID: cluster})
	if err != nil || forward.Counts.Total != 4 || forward.Counts.Direct != 4 || forward.Counts.LowerBound || len(forward.Resources) != 4 || len(forward.Notices) != 0 {
		t.Fatal("canonical references changed scoped forward counts", forward, err)
	}
	prefix := "/spec/template/spec"
	switch kind {
	case "Pod":
		prefix = "/spec"
	case "CronJob":
		prefix = "/spec/jobTemplate/spec/template/spec"
	}
	qualified := 0
	for _, evidence := range forward.ReferenceEvidence {
		wantPath := ""
		switch evidence.Type {
		case graph.EdgeTypeUsesConfigMap:
			wantPath = prefix + "/containers/0/envFrom/0/configMapRef"
		case graph.EdgeTypeMountsVolume:
			wantPath = prefix + "/volumes/0/persistentVolumeClaim"
		default:
			continue
		}
		qualified++
		if evidence.Status != "version_bound" || evidence.SourceUID != string(r.UID) || evidence.SourceResourceVersion != obj.GetResourceVersion() || len(evidence.Uses) != 1 || evidence.Uses[0].FieldPath != wantPath {
			t.Fatal("source explanation used a decoy path or lost version binding", evidence)
		}
	}
	if qualified != 2 {
		t.Fatal("canonical config/claim references lost source explanations", forward.ReferenceEvidence)
	}
	for edgeType, target := range targets {
		reverse, err := analysis.AnalyzeStoredImpact(ctx, base, target.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependents}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil || reverse.Counts.Total != 1 || reverse.Counts.LowerBound || len(reverse.Resources) != 1 || reverse.Resources[0].Resource.ID != r.ID() {
			t.Fatal("canonical target did not produce one scoped dependent", reverse, err)
		}
		path := reverse.Resources[0].Path
		if len(path) != 1 || path[0].From != r.ID() || path[0].To != target.ID() || path[0].Type != edgeType || path[0].TraversalFrom != target.ID() || path[0].TraversalTo != r.ID() {
			t.Fatal("canonical reverse path changed stored/traversal direction", path)
		}
	}
}

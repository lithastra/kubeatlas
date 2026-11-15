// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"encoding/json"
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

type configMapCollectorCase struct {
	kind, group, resource, prefix string
}

func TestCoreConfigMapSourcesReplayAndExplanation(t *testing.T) {
	for _, fixture := range []configMapCollectorCase{
		{"Pod", "", "pods", "/spec"}, {"Deployment", "apps", "deployments", "/spec/template/spec"},
		{"ReplicaSet", "apps", "replicasets", "/spec/template/spec"}, {"StatefulSet", "apps", "statefulsets", "/spec/template/spec"},
		{"DaemonSet", "apps", "daemonsets", "/spec/template/spec"}, {"Job", "batch", "jobs", "/spec/template/spec"},
		{"CronJob", "batch", "cronjobs", "/spec/jobTemplate/spec/template/spec"},
	} {
		for _, cluster := range []string{"east", ""} {
			for _, history := range []bool{false, true} {
				for _, state := range []string{"ready", "write-failure", "missing-target"} {
					t.Run(fmt.Sprintf("%s/cluster=%s/history=%t/%s", fixture.kind, cluster, history, state), func(t *testing.T) {
						testConfigMapSourcesReplay(t, fixture, cluster, history, state)
					})
				}
			}
		}
	}
}

func testConfigMapSourcesReplay(t *testing.T, fixture configMapCollectorCase, cluster string, history bool, state string) {
	t.Helper()
	ctx := context.Background()
	base := memory.New()
	store, sink := &resyncStore{GraphStore: base}, &resyncSink{}
	tracker := operations.NewCoverageTracker(time.Minute)
	gvr := schema.GroupVersionResource{Group: fixture.group, Version: "v1", Resource: fixture.resource}
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
	obj := resyncObject(fixture.kind, "consumer", "consumer-uid", "opaque-version")
	obj.SetAPIVersion(gvr.GroupVersion().String())
	var spec map[string]any
	if err := json.Unmarshal([]byte(`{
	 "containers":[{"name":"main","env":[{"name":"literal-canary","value":"value-canary"}],"volumeMounts":[{"name":"mixed"}]}],
	 "initContainers":[{"envFrom":[{"configMapRef":{"name":"init","optional":true}}],"env":[{"valueFrom":{"configMapKeyRef":{"name":"init","key":"init-key-canary"}}}]}],
	 "ephemeralContainers":[{"env":[{"valueFrom":{"configMapKeyRef":{"name":"ephemeral","key":"debug-key-canary","optional":true}}}]}],
	 "volumes":[{"name":"mixed","projected":{"sources":[{"configMap":{"name":"projected","optional":false}},{"configMap":{"name":"init","optional":true}}]}}]
	}`), &spec); err != nil {
		t.Fatal(err)
	}
	outer := spec
	if fixture.kind == "CronJob" {
		outer = map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": spec}}}}
	} else if fixture.kind != "Pod" {
		outer = map[string]any{"template": map[string]any{"spec": spec}}
	}
	obj.Object["spec"] = outer
	r := UnstructuredToResource(obj, fixture.kind)
	r.ClusterID = cluster
	targets := map[string]graph.Resource{}
	for _, name := range []string{"init", "ephemeral", "projected"} {
		targets[name] = graph.Resource{ClusterID: cluster, Namespace: r.Namespace, Kind: "ConfigMap", Name: name, GroupVersion: "v1", UID: "config-uid", ResourceVersion: "config-version"}
		for _, owner := range []string{"east", "west", ""} {
			if state == "missing-target" && name == "projected" && owner == cluster {
				continue
			}
			row := targets[name]
			row.ClusterID = owner
			row.Raw = map[string]any{"data": map[string]any{"key": "config-content-canary"}}
			if err := base.UpsertResource(ctx, row); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Backfill a source already stored before these paths were supported.
	m.handleUpsert(ctx, gvr, obj, graph.EventTypeAdd)
	if store.resourceWrites.Load() != 1 || store.edgeWrites.Load() != 0 || len(sink.recorded()) != 1 {
		t.Fatal("fixture did not commit one source receipt")
	}
	reg := extractor.New()
	reg.Register(extractor.ConfigMapExtractor{})
	m.extractor = reg
	failure := state == "write-failure"
	store.failEdge.Store(failure)
	m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	wantEdges, wantNotifications := 3, int64(2)
	if failure {
		wantEdges, wantNotifications = 0, 1
	}
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != wantEdges || store.edgeWrites.Load() != 3 || notifications.Load() != wantNotifications {
		t.Fatal("same-version replay lost ConfigMap paths or notified failed writes", edges, store.edgeWrites.Load(), notifications.Load(), err)
	}
	before := coverageView(t, tracker, cluster, gvr)
	if !before.ExtractionGapSince.Equal(prior) || before.PersistenceGapSince.IsZero() == failure || !before.ProcessingGapSince.IsZero() {
		t.Fatal("ConfigMap backfill changed gap qualification", before)
	}
	store.failEdge.Store(false)
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 3 || store.resourceWrites.Load() != 1 || store.edgeWrites.Load() != 9 || len(sink.recorded()) != 1 || notifications.Load() != wantNotifications+2 {
		t.Fatal("replay duplicated resource history or missed edge recovery", edges, sink.recorded(), err)
	}
	for _, edge := range edges {
		if edge.From != r.ID() || edge.Type != graph.EdgeTypeUsesConfigMap || (edge.To != targets["init"].ID() && edge.To != targets["ephemeral"].ID() && edge.To != targets["projected"].ID()) {
			t.Fatal("ConfigMap edge escaped the source scope", edge)
		}
	}
	after := coverageView(t, tracker, cluster, gvr)
	stored, readErr := base.GetResource(ctx, r.ID())
	if readErr != nil || !graph.SameResourceVersion(stored, r) || !after.ExtractionGapSince.Equal(prior) || !after.PersistenceGapSince.Equal(before.PersistenceGapSince) || !after.ProcessingGapSince.IsZero() {
		t.Fatal("backfill changed receipts or cleared known gaps", stored, after, readErr)
	}
	opts := analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependencies}
	limits := graph.ImpactSnapshotOptions{ClusterID: cluster}
	if state == "missing-target" {
		result, err := analysis.AnalyzeStoredImpact(ctx, base, r.ID(), opts, limits)
		if err != nil || result.Counts.Total != 2 || !result.Counts.LowerBound || len(result.Notices) != 1 || result.Notices[0].Reason != analysis.ImpactMissingResource || result.Notices[0].Count != 1 {
			t.Fatal("foreign same-name ConfigMap concealed missing target", result, err)
		}
		if _, err := base.GetResource(ctx, targets["projected"].ID()); !errors.As(err, new(graph.ErrNotFound)) {
			t.Fatal("declaration synthesized a ConfigMap", err)
		}
		if err := base.UpsertResource(ctx, targets["projected"]); err != nil {
			t.Fatal(err)
		}
	}
	result, err := analysis.AnalyzeStoredImpact(ctx, base, r.ID(), opts, limits)
	if err != nil || result.Counts.Total != 3 || result.Counts.Direct != 3 || result.Counts.LowerBound || len(result.ReferenceEvidence) != 3 {
		t.Fatal("new ConfigMap declarations did not reach impact analysis", result, err)
	}
	for _, evidence := range result.ReferenceEvidence {
		assertConfigMapSourceUse(t, evidence, r, fixture.prefix)
	}
	for _, target := range targets {
		reverse, err := analysis.AnalyzeStoredImpact(ctx, base, target.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependents}, limits)
		if err != nil || reverse.Counts.Total != 1 || reverse.Resources[0].Resource.ID != r.ID() || len(reverse.ReferenceEvidence) != 1 {
			t.Fatal("reverse ConfigMap impact lost its consumer", reverse, err)
		}
		assertConfigMapSourceUse(t, reverse.ReferenceEvidence[0], r, fixture.prefix)
	}
	body, err := json.Marshal(result)
	if err != nil || strings.Contains(string(body), "canary") {
		t.Fatal("ConfigMap contents, keys or environment values escaped", string(body), err)
	}
}

func assertConfigMapSourceUse(t *testing.T, e analysis.ImpactReferenceEvidence, r graph.Resource, prefix string) {
	t.Helper()
	want := map[string][]analysis.ImpactReferenceUse{
		"init": {
			{FieldPath: prefix + "/initContainers/0/envFrom/0/configMapRef", Mode: "environment", Optional: "optional", Refresh: "container_restart_required"},
			{FieldPath: prefix + "/initContainers/0/env/0/valueFrom/configMapKeyRef", Mode: "environment", Optional: "required", Refresh: "container_restart_required"},
			{FieldPath: prefix + "/volumes/0/projected/sources/1/configMap", MountFieldPath: prefix + "/containers/0/volumeMounts/0", Mode: "volume_mount", Optional: "optional", Refresh: "eventual_projection"},
		},
		"ephemeral": {{FieldPath: prefix + "/ephemeralContainers/0/env/0/valueFrom/configMapKeyRef", Mode: "environment", Optional: "optional", Refresh: "container_restart_required"}},
		"projected": {{FieldPath: prefix + "/volumes/0/projected/sources/0/configMap", MountFieldPath: prefix + "/containers/0/volumeMounts/0", Mode: "volume_mount", Optional: "required", Refresh: "eventual_projection"}},
	}
	var expected []analysis.ImpactReferenceUse
	for name, uses := range want {
		if e.To == (graph.Resource{ClusterID: r.ClusterID, Namespace: r.Namespace, Kind: "ConfigMap", Name: name}).ID() {
			expected = uses
		}
	}
	if e.From != r.ID() || e.Type != graph.EdgeTypeUsesConfigMap || e.Status != "version_bound" || e.SourceUID != string(r.UID) || e.SourceResourceVersion != r.ResourceVersion || expected == nil || !reflect.DeepEqual(e.Uses, expected) {
		t.Fatal("ConfigMap reference lost version-bound paths/flags", e, expected)
	}
}

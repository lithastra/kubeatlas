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
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCoreMalformedSelectorRecoveryAndImpact(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, mode := range []string{"service", "selects", "ingress", "egress"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, history, mode), func(t *testing.T) {
					testCoreMalformedSelector(t, cluster, history, mode, false)
				})
			}
		}
	}
}

func testCoreMalformedSelector(t *testing.T, cluster string, history bool, mode string, expressions bool) {
	t.Helper()
	ctx := context.Background()
	base := memory.New()
	store, sink := &resyncStore{GraphStore: base}, &resyncSink{}
	kind := "NetworkPolicy"
	gvr := schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}
	if mode == "service" {
		kind, gvr = "Service", schema.GroupVersionResource{Version: "v1", Resource: "services"}
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
	token.RecordGap(gvr, operations.CoveragePersistenceFailed, prior)
	var notifications atomic.Int64
	m := NewInformerManager(nil, store, WithClusterID(cluster), WithExtractor(extractor.Default()), WithSnapshotSink(sink), WithMemberRepair(NewMemberRepair(nil, store, cluster)), WithBroadcaster(func(_, _, _, _ string) { notifications.Add(1) }))
	m.coverageSession = token
	if history {
		cancellationHistory(m, store, cluster, sink)
	}
	obj := resyncObject(kind, "source", "source-uid", "version-one")
	obj.SetAPIVersion(gvr.GroupVersion().String())
	good := map[string]any{"matchLabels": map[string]any{"app": "web"}}
	bad := map[string]any{"matchLabels": map[string]any{"app": "web", "invalid": int64(7)}}
	if expressions {
		good = map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "In", "values": []any{"web"}}}}
		bad = map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "In", "values": []any{"web"}}, nil}}
	}
	field, policyType := "from", "Ingress"
	direction := "ingress"
	if mode == "egress" {
		field, policyType, direction = "to", "Egress", "egress"
	}
	peers := []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"role": "prefix"}}}, map[string]any{"podSelector": bad}}
	spec := map[string]any{"podSelector": good, "policyTypes": []any{policyType}, direction: []any{map[string]any{field: peers}}}
	wantInitial := 2 // Independent SELECTS_NP and the earlier valid peer.
	switch mode {
	case "service":
		spec, wantInitial = map[string]any{"selector": bad["matchLabels"]}, 0
	case "selects":
		spec["podSelector"], spec[direction], wantInitial = bad, []any{map[string]any{field: []any{map[string]any{"podSelector": good}}}}, 1
	}
	obj.Object["spec"] = spec
	r := UnstructuredToResource(obj, kind)
	r.ClusterID = cluster
	selected := graph.Resource{ClusterID: cluster, Kind: "Pod", Namespace: r.Namespace, Name: "selected", GroupVersion: "v1", UID: "pod-uid", ResourceVersion: "pod-version", Labels: map[string]string{"app": "web"}}
	decoy := selected
	decoy.Name, decoy.UID, decoy.Labels = "decoy", "decoy-uid", map[string]string{"other": ""}
	prefix := selected
	prefix.Name, prefix.UID, prefix.Labels = "prefix", "prefix-uid", map[string]string{"role": "prefix"}
	for _, row := range []graph.Resource{selected, decoy, prefix} {
		for _, owner := range []string{"east", "west", ""} {
			copy := row
			copy.ClusterID = owner
			if err := base.UpsertResource(ctx, copy); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, event := range []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate} {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), event)
	}
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != wantInitial || store.edgeWrites.Load() != int64(2*wantInitial) || store.resourceWrites.Load() != 1 || len(sink.recorded()) != 1 {
		t.Fatal("malformed replay broadened edges or lost its committed receipt/prefix", edges, sink.recorded(), err)
	}
	before := coverageView(t, tracker, cluster, gvr)
	if before.ExtractionGapSince.IsZero() || !before.PersistenceGapSince.Equal(prior) || !before.ProcessingGapSince.IsZero() {
		t.Fatal("malformed selector did not preserve distinct gap evidence", before)
	}
	// Recovery is a fresh valid source version, not an edit to an old receipt.
	obj.SetResourceVersion("version-two")
	switch mode {
	case "service":
		spec["selector"] = good["matchLabels"]
	case "selects":
		spec["podSelector"] = good
	default:
		peers[1] = map[string]any{"podSelector": good}
	}
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	wantFinal := wantInitial + 1
	if err != nil || len(edges) != wantFinal || store.edgeWrites.Load() != int64(2*wantInitial+2*wantFinal) || store.resourceWrites.Load() != 2 || len(sink.recorded()) != 2 {
		t.Fatal("valid version/replay failed to recover without duplicate resource history", edges, sink.recorded(), err)
	}
	wantNotifications := int64(4)
	if mode == "service" {
		wantNotifications = 3 // Invalid unchanged replay committed no writes.
	}
	if notifications.Load() != wantNotifications || sink.recorded()[0].ResourceVersion != "version-one" || sink.recorded()[1].ResourceVersion != "version-two" {
		t.Fatal("selector recovery changed committed notification/history semantics", notifications.Load(), sink.recorded())
	}
	after := coverageView(t, tracker, cluster, gvr)
	if !after.ExtractionGapSince.Equal(before.ExtractionGapSince) || !after.PersistenceGapSince.Equal(prior) || !after.ProcessingGapSince.IsZero() {
		t.Fatal("valid selector erased prior loss evidence", after)
	}
	for _, edge := range edges {
		if edge.From != r.ID() || (edge.To != selected.ID() && edge.To != prefix.ID()) {
			t.Fatal("selector attached an unproven or foreign target", edge)
		}
	}
	for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependencies, analysis.ImpactDependents} {
		root, target := r.ID(), selected.ID()
		if relation == analysis.ImpactDependents {
			root, target = target, root
		}
		result, err := analysis.AnalyzeStoredImpact(ctx, base, root, analysis.ImpactOptions{ClusterID: cluster, Relation: relation}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil {
			t.Fatal(err)
		}
		if mode != "service" {
			noticeCount := wantFinal
			if relation == analysis.ImpactDependents {
				noticeCount = 2 // Only SELECTS_NP and the peer ending at selected.
			}
			if result.Counts.Total != 0 || len(result.Resources) != 0 || len(result.Notices) != 1 || result.Notices[0].Reason != analysis.ImpactUnmodeledRelationship || result.Notices[0].Count != noticeCount {
				t.Fatal("NetworkPolicy edges entered ordinary impact or lost their exclusion notice", result)
			}
			continue
		}
		if result.Counts.Total != 1 || len(result.Resources) != 1 || result.Resources[0].Resource.ID != target || len(result.Resources[0].Path) != 1 {
			t.Fatal("valid source recovery did not yield scoped impact paths", result, err)
		}
		step := result.Resources[0].Path[0]
		if step.From != r.ID() || step.To != selected.ID() || step.Type != graph.EdgeTypeSelects || step.TraversalFrom != root || step.TraversalTo != target {
			t.Fatal("recovered Service path changed edge or traversal identity", step)
		}
	}
	result, err := analysis.AnalyzeStoredImpact(ctx, base, decoy.ID(), analysis.ImpactOptions{ClusterID: cluster, Relation: analysis.ImpactDependents}, graph.ImpactSnapshotOptions{ClusterID: cluster})
	if err != nil || result.Counts.Total != 0 || len(result.Resources) != 0 {
		t.Fatal("malformed selector left a false dependent", result, err)
	}
}

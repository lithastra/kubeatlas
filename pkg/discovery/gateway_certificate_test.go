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

func TestCoreGatewayCertificateGroupReplayAndReferenceBoundary(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, state := range []string{"core-alias", "foreign-group", "malformed-group", "write-failure"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, history, state), func(t *testing.T) {
					testGatewayCertificateGroup(t, cluster, history, state)
				})
			}
		}
	}
}

func testGatewayCertificateGroup(t *testing.T, cluster string, history bool, state string) {
	t.Helper()
	ctx := context.Background()
	base := memory.New()
	store, sink := &resyncStore{GraphStore: base}, &resyncSink{}
	tracker := operations.NewCoverageTracker(time.Minute)
	gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
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
	obj := resyncObject("Gateway", "gateway", "gateway-uid", "version-one")
	obj.SetAPIVersion(gvr.GroupVersion().String())
	obj.Object["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{"kind": "ConfigMap", "name": "controller", "apiVersion": "v1", "uid": "owner-uid"}}
	reference := map[string]any{"name": "selected", "namespace": "certs", "group": ""}
	batchSize := 4 // Three certificate references plus an independent owner edge.
	switch state {
	case "core-alias":
		reference["group"], batchSize = "core", 3
	case "foreign-group":
		reference["group"], batchSize = "example.org", 3
	case "malformed-group":
		reference["group"], batchSize = map[string]any{"private": "certificate-group-canary"}, 2
	}
	obj.Object["spec"] = map[string]any{"listeners": []any{
		map[string]any{"tls": map[string]any{"certificatePayload": "inline-certificate-canary", "certificateRefs": []any{map[string]any{"name": "confirmed", "namespace": "certs"}, reference}}},
		map[string]any{"tls": map[string]any{"certificateRefs": []any{map[string]any{"name": "later", "namespace": "certs"}}}},
	}}
	r := UnstructuredToResource(obj, "Gateway")
	r.ClusterID = cluster
	selected := graph.SecretReferenceResource("certs", "selected", cluster)
	for _, scope := range []string{"east", "west", ""} {
		owner := graph.Resource{ClusterID: scope, Kind: "ConfigMap", Namespace: r.Namespace, Name: "controller", GroupVersion: "v1", UID: "owner-uid", ResourceVersion: "owner-version"}
		if err := base.UpsertResource(ctx, owner); err != nil {
			t.Fatal(err)
		}
		if scope != cluster {
			if err := base.UpsertResource(ctx, graph.SecretReferenceResource("certs", "selected", scope)); err != nil {
				t.Fatal(err)
			}
		}
	}
	store.failEdge.Store(state == "write-failure")
	for _, event := range []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate} {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), event)
	}
	edges, err := base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	wantEdges := batchSize
	if state == "write-failure" {
		wantEdges = 0 // Confirmed placeholder writes still remain retained.
	}
	if err != nil || len(edges) != wantEdges || store.resourceWrites.Load() != int64(1+2*(batchSize-1)) || store.edgeWrites.Load() != int64(2*batchSize) || len(sink.recorded()) != 1 || notifications.Load() != 2 {
		t.Fatal("certificate replay lost source/placeholder receipts or duplicated history", edges, sink.recorded(), err)
	}
	if state != "write-failure" {
		if _, err := base.GetResource(ctx, selected.ID()); !errors.As(err, new(graph.ErrNotFound)) {
			t.Fatal("unsupported/malformed group created a local Secret placeholder", err)
		}
	}
	before := coverageView(t, tracker, cluster, gvr)
	if before.ExtractionGapSince.IsZero() != (state != "malformed-group") || before.PersistenceGapSince.IsZero() != (state != "write-failure") || !before.ProcessingGapSince.Equal(prior) {
		t.Fatal("certificate failure lost distinct gap evidence", before)
	}
	wantSourceWrites := int64(1)
	if state != "write-failure" {
		reference["group"] = ""
		obj.SetResourceVersion("version-two")
		wantSourceWrites = 2
	}
	store.failEdge.Store(false)
	for range 2 {
		m.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	edges, err = base.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 4 || store.resourceWrites.Load() != wantSourceWrites+int64(2*(batchSize-1)+6) || store.edgeWrites.Load() != int64(2*batchSize+8) || len(sink.recorded()) != int(wantSourceWrites) || notifications.Load() != 4 {
		t.Fatal("valid certificate recovery lost edges or manufactured resource history", edges, sink.recorded(), err)
	}
	for _, name := range []string{"confirmed", "selected", "later"} {
		want := graph.SecretReferenceResource("certs", name, cluster)
		got, err := base.GetResource(ctx, want.ID())
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("certificate target retained more than a reference-only identity", got, err)
		}
	}
	for _, event := range sink.recorded() {
		if event.Kind != "Gateway" || event.Data != nil || event.ClusterID != cluster || event.UID != string(r.UID) {
			t.Fatal("certificate collection added Secret history or payloads", event)
		}
	}
	stored, err := base.GetResource(ctx, r.ID())
	if err != nil || stored.ResourceVersion != obj.GetResourceVersion() || stored.UID != r.UID {
		t.Fatal("certificate recovery changed its committed source identity", stored, err)
	}
	after := coverageView(t, tracker, cluster, gvr)
	if !after.ExtractionGapSince.Equal(before.ExtractionGapSince) || !after.PersistenceGapSince.Equal(before.PersistenceGapSince) || !after.ProcessingGapSince.Equal(prior) {
		t.Fatal("certificate recovery cleared prior gap evidence", after)
	}
	for _, relation := range []analysis.ImpactRelation{analysis.ImpactDependencies, analysis.ImpactDependents} {
		root, target, count := r.ID(), selected.ID(), 4
		if relation == analysis.ImpactDependents {
			root, target, count = target, root, 1
		}
		result, err := analysis.AnalyzeStoredImpact(ctx, base, root, analysis.ImpactOptions{ClusterID: cluster, Relation: relation}, graph.ImpactSnapshotOptions{ClusterID: cluster})
		if err != nil || result.Counts.Total != count || result.Counts.Direct != count || len(result.Resources) != count || result.Counts.LowerBound {
			t.Fatal("certificate references produced incorrect scoped impact paths", result, err)
		}
		if relation == analysis.ImpactDependents && (!result.Root.ReferenceOnly || result.Root.UID != "" || result.Root.ResourceVersion != "" || result.Root.GroupVersion != "") {
			t.Fatal("Secret impact root escaped the reference-only boundary", result.Root)
		}
		found := false
		for _, item := range result.Resources {
			if item.Resource.ID != target {
				continue
			}
			found = true
			if len(item.Path) != 1 {
				t.Fatal("certificate path acquired another hop", item)
			}
			step := item.Path[0]
			if step.From != r.ID() || step.To != selected.ID() || step.Type != graph.EdgeTypeUsesSecret || step.TraversalFrom != root || step.TraversalTo != target {
				t.Fatal("certificate path changed edge or traversal identity", step)
			}
			if relation == analysis.ImpactDependencies && !item.Resource.ReferenceOnly {
				t.Fatal("Secret impact target was presented as an observed object", item.Resource)
			}
		}
		wire, err := json.Marshal(result)
		if err != nil || !found || strings.Contains(string(wire), "inline-certificate-canary") || strings.Contains(string(wire), "certificate-group-canary") {
			t.Fatal("certificate impact lost its reference or included ignored input values", result, err)
		}
	}
}

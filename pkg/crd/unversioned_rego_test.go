// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	regorules "github.com/lithastra/kubeatlas/pkg/extractor/rego"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func TestCRDUnversionedRegoRetainsFreshRelationships(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, missing := range []string{"uid", "version", "api-version"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/missing=%s", cluster, history, missing), func(t *testing.T) {
					testCRDUnversionedRego(t, cluster, history, missing)
				})
			}
		}
	}
}

func testCRDUnversionedRego(t *testing.T, cluster string, history bool, missing string) {
	t.Helper()
	ctx := context.Background()
	store := memory.New()
	router := regorules.NewRouter()
	router.Register("identity", &regorules.ModuleSpec{Name: "labels"})
	metrics := regorules.NewMetrics()
	cache, err := regorules.NewCache(10, metrics)
	if err != nil {
		t.Fatal(err)
	}
	engine := regorules.New(regorules.WithRouter(router), regorules.WithCache(cache), regorules.WithMetrics(metrics))
	source := strings.Replace(crdPartialModule("target"), `"name":"target"`, `"name":input.metadata.labels.target`, 1)
	if err := engine.LoadModule(ctx, "identity/labels", source, "data.partial.derive"); err != nil {
		t.Fatal(err)
	}
	tracker := operations.NewCoverageTracker(time.Minute)
	session, err := tracker.BeginSource(cluster, operations.CoverageSourceCRD, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := session.BeginType(coverageFooGVR, time.Now())
	if err != nil || !token.BindOrigin(graph.ResourceOrigin{OwnerName: "foos.example.com", OwnerUID: "definition-uid", Group: "example.com", Version: "v1", Resource: "foos", Kind: "Foo", Namespaced: true}) {
		t.Fatal("could not bind source receipt", err)
	}
	token.InitialDeliveryComplete(coverageFooGVR)
	token.ListResult(coverageFooGVR, true, false, time.Now())
	token.WatchStarted(coverageFooGVR)
	sink := &dynamicHistorySink{}
	d := New(nil, store, WithClusterID(cluster), WithMemberRepair(discovery.NewMemberRepair(nil, store, cluster)), WithRegoEvaluator(engine))
	if history {
		d.resourceHistory = discovery.NewResourceHistory(store, cluster, sink)
		if cluster == "" {
			d.resourceHistory = discovery.NewStandaloneResourceHistory(store, sink)
		}
	}
	_, full := coverageObjects(t)
	foreign := discovery.UnstructuredToResource(full, "Foo")
	foreign.ClusterID = "west"
	if err := store.UpsertResource(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	obj := full.DeepCopy()
	switch missing {
	case "uid":
		obj.SetUID("")
	case "version":
		obj.SetResourceVersion("")
	case "api-version":
		obj.SetAPIVersion("")
	}
	r := discovery.UnstructuredToResource(obj, "Foo")
	r.ClusterID = cluster
	checkEdges := func(want int) {
		t.Helper()
		edges, err := store.ListEdges(ctx, r.ID(), graph.DirectionOutgoing)
		if err != nil || len(edges) != want {
			t.Fatal("incomplete identity discarded fresh relationships", edges, err)
		}
		for _, edge := range edges {
			if edge.From != r.ID() || (cluster != "" && !strings.HasPrefix(edge.To, cluster+":")) {
				t.Fatal("unversioned extraction escaped member scope", edge)
			}
		}
	}
	for i, target := range []string{"first", "second"} {
		obj.SetLabels(map[string]string{"target": target})
		d.handleUpsertEvent(ctx, coverageFooGVR, "Foo", obj, token, graph.EventTypeUpdate)
		checkEdges(i + 1) // Existing edges stay retained; no deletion is inferred.
	}
	stored, err := store.GetResource(ctx, r.ID())
	if err != nil || stored.Origin != (graph.ResourceOrigin{}) || len(sink.events) != 0 || cache.Len() != 0 || metrics.Snapshot() != (regorules.MetricsSnapshot{}) {
		t.Fatal("unqualified observation gained receipts, history or cache entries", stored, sink.events, metrics.Snapshot(), err)
	}
	row := coverageRow(memberCRDSource(t, tracker, cluster), coverageFooGVR)
	if row.State != operations.CoveragePartial || row.ProcessingGapSince.IsZero() || !row.ExtractionGapSince.IsZero() || !row.PersistenceGapSince.IsZero() {
		t.Fatal("successful unversioned extraction lost identity-gap qualification", row)
	}
	full.SetLabels(map[string]string{"target": "qualified"})
	d.handleUpsertEvent(ctx, coverageFooGVR, "Foo", full, token, graph.EventTypeUpdate)
	d.handleUpsertEvent(ctx, coverageFooGVR, "Foo", full.DeepCopy(), token, graph.EventTypeUpdate)
	checkEdges(3)
	stored, err = store.GetResource(ctx, r.ID())
	if err != nil || !stored.Origin.Matches(stored) || cache.Len() != 1 || metrics.Snapshot().CacheMisses != 1 || metrics.Snapshot().CacheHits != 1 {
		t.Fatal("qualified recovery failed to restore receipts and cache", stored, metrics.Snapshot(), err)
	}
	after := coverageRow(memberCRDSource(t, tracker, cluster), coverageFooGVR)
	if !after.ProcessingGapSince.Equal(row.ProcessingGapSince) || after.State != operations.CoveragePartial || !after.ExtractionGapSince.IsZero() || !after.PersistenceGapSince.IsZero() {
		t.Fatal("qualified recovery erased or misclassified prior loss", after)
	}
	wantEvents := 0
	if history {
		wantEvents = 1
	}
	if len(sink.events) != wantEvents {
		t.Fatal("unversioned replay manufactured or duplicated history", sink.events)
	}
	for _, event := range sink.events {
		if event.Data != nil || event.ClusterID != cluster || event.UID != string(full.GetUID()) || event.ResourceVersion != full.GetResourceVersion() {
			t.Fatal("qualified recovery changed metadata-only history", event)
		}
	}
	if got, err := store.GetResource(ctx, foreign.ID()); err != nil || got.UID != foreign.UID || got.ClusterID != foreign.ClusterID {
		t.Fatal("unversioned recovery changed foreign data", got, err)
	}
	if edges, err := store.ListEdges(ctx, foreign.ID(), graph.DirectionOutgoing); err != nil || len(edges) != 0 {
		t.Fatal("unversioned recovery wrote foreign edges", edges, err)
	}
}

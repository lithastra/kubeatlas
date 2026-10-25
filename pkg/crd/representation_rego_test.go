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

func TestCRDRegoEndpointSwitchPreservesRepresentationBoundary(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/reverse=%t", cluster, history, reverse), func(t *testing.T) {
					testCRDRegoEndpointSwitch(t, cluster, history, reverse)
				})
			}
		}
	}
}

func testCRDRegoEndpointSwitch(t *testing.T, cluster string, history, reverse bool) {
	t.Helper()
	ctx := context.Background()
	store := memory.New()
	router := regorules.NewRouter()
	router.Register("representation", &regorules.ModuleSpec{Name: "version", Match: regorules.GVKMatch{Group: "example.com", Kind: "Foo"}})
	metrics := regorules.NewMetrics()
	cache, err := regorules.NewCache(10, metrics)
	if err != nil {
		t.Fatal(err)
	}
	engine := regorules.New(regorules.WithRouter(router), regorules.WithCache(cache), regorules.WithMetrics(metrics))
	source := strings.Replace(crdPartialModule("target"), `"name":"target"`, `"name":replace(input.apiVersion, "/", "-")`, 1)
	if err := engine.LoadModule(ctx, "representation/version", source, "data.partial.derive"); err != nil {
		t.Fatal(err)
	}
	tracker := operations.NewCoverageTracker(time.Minute)
	session, err := tracker.BeginSource(cluster, operations.CoverageSourceCRD, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sink := &dynamicHistorySink{}
	d := New(nil, store, WithClusterID(cluster), WithMemberRepair(discovery.NewMemberRepair(nil, store, cluster)), WithRegoEvaluator(engine))
	if history {
		d.resourceHistory = discovery.NewResourceHistory(store, cluster, sink)
		if cluster == "" {
			d.resourceHistory = discovery.NewStandaloneResourceHistory(store, sink)
		}
	}
	_, obj := coverageObjects(t)
	versions := []string{"v1", "v2"}
	if reverse {
		versions[0], versions[1] = versions[1], versions[0]
	}
	var original graph.Resource
	var currentOrigin graph.ResourceOrigin
	for i, version := range versions {
		gvr := coverageFooGVR
		gvr.Version = version
		token, err := session.BeginType(gvr, time.Now())
		if err != nil || !token.BindOrigin(graph.ResourceOrigin{OwnerName: "foos.example.com", OwnerUID: "definition-uid", Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Kind: "Foo", Namespaced: true}) {
			t.Fatal("could not bind endpoint receipt", err)
		}
		current := obj.DeepCopy()
		current.SetAPIVersion(gvr.GroupVersion().String())
		observed := discovery.UnstructuredToResource(current, "Foo")
		observed.ClusterID = cluster
		currentOrigin = token.StampOrigin(observed).Origin
		d.handleUpsertEvent(ctx, gvr, "Foo", current, token, graph.EventTypeAdd)
		if i == 0 {
			r := discovery.UnstructuredToResource(current, "Foo")
			r.ClusterID = cluster
			original, err = store.GetResource(ctx, r.ID())
			if err != nil || !original.Origin.Matches(original) {
				t.Fatal("initial endpoint failed to persist its receipt", original, err)
			}
			token.Stop(time.Now()) // Endpoint retirement does not prove object deletion.
		} else {
			d.handleUpsertEvent(ctx, gvr, "Foo", current.DeepCopy(), token, graph.EventTypeUpdate)
		}
	}
	edges, err := store.ListEdges(ctx, original.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 2 {
		t.Fatal("endpoint switch reused the former API representation", edges, err)
	}
	targets := make(map[string]bool)
	for _, edge := range edges {
		if edge.From != original.ID() {
			t.Fatal("representation edges escaped member scope", edge)
		}
		targets[edge.To] = true
	}
	for _, version := range versions {
		want := (graph.Resource{ClusterID: cluster, Namespace: "default", Kind: "Bar", Name: "example.com-" + version}).ID()
		if !targets[want] {
			t.Fatal("missing representation-specific relationship", want, targets)
		}
	}
	retained, err := store.GetResource(ctx, original.ID())
	// Existing fresh-delivery semantics update the current representation and
	// writer receipt across API versions, without adding UID/version history.
	if err != nil || retained.UID != original.UID || retained.ResourceVersion != original.ResourceVersion || retained.GroupVersion != "example.com/"+versions[1] || retained.Origin != currentOrigin || !retained.Origin.Matches(retained) {
		t.Fatal("cache separation changed fresh representation receipts", retained, err)
	}
	wantEvents := 0
	if history {
		wantEvents = 1
	}
	if len(sink.events) != wantEvents {
		t.Fatal("representation switch manufactured resource history", sink.events)
	}
	for _, event := range sink.events {
		if event.Data != nil || event.ClusterID != cluster || event.UID != string(original.UID) || event.ResourceVersion != original.ResourceVersion {
			t.Fatal("representation switch changed metadata-only history", event)
		}
	}
	m := metrics.Snapshot()
	if m.CacheMisses != 2 || m.CacheHits != 1 || cache.Len() != 2 {
		t.Fatal("representation switch lost per-version cache replay", cache.Len(), m)
	}
}

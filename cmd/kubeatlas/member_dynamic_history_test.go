// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// Both sources observe the constraint; the core fixture deliberately also
// watches it to exercise all three ingress paths using the production factory.
func dynamicHistoryClient(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
	c := memberDynamicClient(t)
	definition := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{"name": "k8srequiredlabels.constraints.gatekeeper.sh", "uid": "constraint-crd-uid"},
		"spec":     map[string]any{"group": "constraints.gatekeeper.sh", "names": map[string]any{"kind": "K8sRequiredLabels", "plural": "k8srequiredlabels"}, "scope": "Cluster", "versions": []any{map[string]any{"name": "v1beta1", "served": true, "storage": true}}},
	}}
	if err := c.Tracker().Create(memberCRDs, definition, ""); err != nil {
		t.Fatal(err)
	}

	return c
}

type memberHistoryExtractorFunc func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error)

func (f memberHistoryExtractorFunc) ExtractAll(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	return f(ctx, r, q)
}

type memberHistoryRegoFunc func(context.Context, graph.Resource) ([]graph.Edge, error)

func (f memberHistoryRegoFunc) EvaluateForResource(ctx context.Context, r graph.Resource) ([]graph.Edge, error) {
	return f(ctx, r)
}

func TestMemberFactoryDynamicHistoryOverlapRepairAndReattachment(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	writer := snapshot.New(store, snapshot.Config{Workers: 2, QueueSize: 64}, nil)
	writer.Start(ctx)
	t.Cleanup(writer.Stop)
	tracker := operations.NewCoverageTracker(time.Minute)
	var synced atomic.Int64
	// A committed history event acknowledges only the first overlapping source.
	// Extraction runs after each source's receipt handling, including replays.
	// Observe every source explicitly before advancing to the next watch phase.
	type delivery struct{ source, cluster, kind, version string }
	var deliveryMu sync.Mutex
	delivered := map[delivery]bool{}
	observe := func(source string, r graph.Resource) {
		if r.Name != "same" {
			return
		}
		deliveryMu.Lock()
		defer deliveryMu.Unlock()
		delivered[delivery{source, r.ClusterID, r.Kind, r.ResourceVersion}] = true
	}
	ext := extractor.Default()
	probe := func(source string) memberHistoryExtractorFunc {
		return func(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
			edges, err := ext.ExtractAll(ctx, r, q)
			if err == nil {
				observe(source, r)
			}
			return edges, err
		}
	}
	rules := memberHistoryRegoFunc(func(_ context.Context, r graph.Resource) ([]graph.Edge, error) {
		observe("crd", r)
		return nil, nil
	})
	factory := memberInformerFactory([]discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{memberPods, memberConstraints}), discovery.WithOnSynced(func() { synced.Add(1) }), discovery.WithExtractor(probe("core"))}, tracker, rules, probe("gatekeeper"), discovery.NewDynamicMetrics(), nil, writer)
	allDelivered := func(cluster, version string, initial bool) bool {
		deliveryMu.Lock()
		defer deliveryMu.Unlock()
		for _, source := range []string{"core", "crd", "gatekeeper"} {
			if !delivered[delivery{source, cluster, "K8sRequiredLabels", version}] {
				return false
			}
		}
		return delivered[delivery{"crd", cluster, "Foo", version}] && (!initial || delivered[delivery{"core", cluster, "Pod", version}])
	}
	for _, r := range []graph.Resource{
		{ClusterID: "east", Kind: "Foo", Namespace: "default", Name: "gone", UID: "missing-foo", ResourceVersion: "old", GroupVersion: "example.com/v1"},
		{ClusterID: "east", Kind: "K8sRequiredLabels", Name: "gone", UID: "missing-constraint", ResourceVersion: "old", GroupVersion: "constraints.gatekeeper.sh/v1beta1"},
	} {
		if err := store.UpsertResource(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	events := func(cluster string) []graph.ResourceEvent {
		t.Helper()
		all, err := store.ListEvents(ctx, "", time.Time{}, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		out := []graph.ResourceEvent{}
		for _, e := range all {
			if e.ClusterID == cluster {
				out = append(out, e)
			}
		}
		return out
	}
	start := func(name string, c *dynamicfake.FakeDynamicClient) func() {
		memberCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- factory(name, c, memberMetadata{}, store).Start(memberCtx) }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Errorf("member exit: %v", err)
					}
				case <-time.After(4 * time.Second):
					t.Error("member recording did not drain")
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	east, west := dynamicHistoryClient(t), dynamicHistoryClient(t)
	stopEast, stopWest := start("east", east), start("west", west)
	awaitMember(t, "all member sources and repaired deletes", func() bool {
		if synced.Load() != 2 || len(events("east")) < 5 || len(events("west")) < 3 || !allDelivered("east", "1", true) || !allDelivered("west", "1", true) {
			return false
		}
		for _, name := range []string{"east", "west"} {
			sources := memberSources(t, tracker, name)
			if sources[operations.CoverageSourceCRD].State != operations.CoverageObserved || sources[operations.CoverageSourceGatekeeper].State != operations.CoverageObserved {
				return false
			}
		}
		return true
	})
	for _, gvr := range []schema.GroupVersionResource{memberFoos, memberConstraints} {
		ns := ""
		if gvr == memberFoos {
			ns = "default"
		}
		obj, err := east.Resource(gvr).Namespace(ns).Get(ctx, "same", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		obj.SetResourceVersion("2")
		obj.Object["spec"] = map[string]any{"private": "history-payload-canary"}
		if _, err := east.Resource(gvr).Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	awaitMember(t, "all overlapping update handlers and two committed updates", func() bool {
		if !allDelivered("east", "2", false) {
			return false
		}
		count := 0
		for _, e := range events("east") {
			if e.EventType == graph.EventTypeUpdate {
				count++
			}
		}
		return count >= 2
	})
	if err := east.Resource(memberConstraints).Delete(ctx, "same", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitMember(t, "constraint delete committed", func() bool {
		_, err := store.GetResource(ctx, "east:/K8sRequiredLabels/same")
		return errors.As(err, new(graph.ErrNotFound))
	})
	// Retiring metadata is not evidence that retained instances were deleted.
	if err := east.Resource(memberCRDs).Delete(ctx, "foos.example.com", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := east.Resource(memberTemplates).Delete(ctx, "k8srequiredlabels", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	stopEast()
	stopWest()
	if _, err := store.GetResource(ctx, "east:default/Foo/same"); err != nil {
		t.Fatal("retirement removed retained Foo")
	}
	check := func(cluster string, want int) {
		t.Helper()
		es := events(cluster)
		if len(es) != want {
			t.Fatalf("%s got %d history events, want %d: %+v", cluster, len(es), want, es)
		}
		seen := map[string]bool{}
		for _, e := range es {
			key := e.Kind + "/" + e.Name + "/" + string(e.EventType) + "/" + e.ResourceVersion
			if seen[key] || e.Data != nil {
				t.Fatal("duplicate history or retained payload")
			}
			seen[key] = true
		}
	}
	check("east", 8)
	check("west", 3)
	for _, e := range events("east") {
		if e.Name == "gone" && e.EventType != graph.EventTypeDelete {
			t.Fatal("repair manufactured creation")
		}
	}
	previousGeneration := memberSources(t, tracker, "east")[operations.CoverageSourceCRD].Generation
	deliveryMu.Lock()
	for key := range delivered {
		if key.cluster == "east" {
			delete(delivered, key)
		}
	}
	deliveryMu.Unlock()
	stopFresh := start("east", east)
	awaitMember(t, "reattached core receipt replay and empty CRD delivery", func() bool {
		deliveryMu.Lock()
		replayed := delivered[delivery{"core", "east", "Pod", "1"}]
		deliveryMu.Unlock()
		source := memberSources(t, tracker, "east")[operations.CoverageSourceCRD]
		if synced.Load() != 3 || source.Generation <= previousGeneration || !replayed {
			return false
		}
		for _, row := range source.Resources {
			if row.Resource == memberConstraints.Resource && row.InitialDeliveryDone {
				return true
			}
		}
		return false
	})
	stopFresh()
	// Foo's CRD was retired above: retain its graph row without restarting
	// its collection or mistaking an old probe receipt for a fresh replay.
	deliveryMu.Lock()
	retiredReplayed := delivered[delivery{"crd", "east", "Foo", "2"}]
	deliveryMu.Unlock()
	if retiredReplayed {
		t.Fatal("reattachment resumed a retired CRD")
	}
	if r, err := store.GetResource(ctx, "east:default/Foo/same"); err != nil || r.ResourceVersion != "2" {
		t.Fatal("reattachment changed the retained retired resource", err)
	}
	check("east", 8)
}

// No generic constraint CRD is present: this deletion must come from the
// Gatekeeper repair callback rather than a coincident core/CRD observer.
func TestMemberFactoryGatekeeperOnlyRepairRecordsHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := memory.New()
	gone := graph.Resource{ClusterID: "east", Kind: "K8sRequiredLabels", Name: "gone", UID: "missed-delete", ResourceVersion: "old", GroupVersion: "constraints.gatekeeper.sh/v1beta1"}
	if err := store.UpsertResource(ctx, gone); err != nil {
		t.Fatal(err)
	}
	writer := snapshot.New(store, snapshot.Config{QueueSize: 16, Workers: 1}, nil)
	writer.Start(ctx)
	t.Cleanup(writer.Stop)
	factory := memberInformerFactory([]discovery.InformerOption{discovery.WithGVRs([]schema.GroupVersionResource{memberPods})}, nil, nil, extractor.Default(), discovery.NewDynamicMetrics(), nil, writer)
	done := make(chan error, 1)
	client := memberDynamicClient(t)
	go func() { done <- factory("east", client, memberMetadata{}, store).Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("member exit: %v", err)
			}
		case <-time.After(4 * time.Second):
			t.Error("member did not join")
		}
	})
	awaitMember(t, "Gatekeeper missed-delete history", func() bool {
		es, err := store.ListEvents(ctx, "", time.Time{}, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			if e.Name == gone.Name && e.UID == string(gone.UID) && e.Kind == gone.Kind && e.ClusterID == "east" && e.EventType == graph.EventTypeDelete {
				return true
			}
		}
		return false
	})
	if _, err := store.GetResource(ctx, gone.ID()); !errors.As(err, new(graph.ErrNotFound)) {
		t.Fatal("repair event preceded deletion")
	}
}

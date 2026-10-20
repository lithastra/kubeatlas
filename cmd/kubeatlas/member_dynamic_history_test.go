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

func TestMemberFactoryDynamicHistoryOverlapRepairAndReattachment(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	writer := snapshot.New(store, snapshot.Config{Workers: 2, QueueSize: 64}, nil)
	writer.Start(ctx)
	t.Cleanup(writer.Stop)
	tracker := operations.NewCoverageTracker(time.Minute)
	var synced atomic.Int64
	factory := memberInformerFactory([]discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{memberPods, memberConstraints}), discovery.WithOnSynced(func() { synced.Add(1) })}, tracker, nil, extractor.Default(), discovery.NewDynamicMetrics(), nil, writer)
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
		if synced.Load() != 2 || len(events("east")) < 5 || len(events("west")) < 3 {
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
	awaitMember(t, "two actual updates", func() bool {
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
	stopFresh := start("east", east)
	awaitMember(t, "reattached core and CRD receipt replay", func() bool {
		source := memberSources(t, tracker, "east")[operations.CoverageSourceCRD]
		if synced.Load() != 3 || source.Generation <= previousGeneration {
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

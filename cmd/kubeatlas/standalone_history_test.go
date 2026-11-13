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
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestStandaloneFactoryDynamicHistoryOverlapAndRestart(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) { runStandaloneHistory(t, enabled) })
	}
}
func runStandaloneHistory(t *testing.T, enabled bool) {
	ctx := context.Background()
	store := memory.New()
	foreign := graph.Resource{ClusterID: "east", Namespace: "default", Kind: "Foo", Name: "same", UID: "same-foo-uid", GroupVersion: "example.com/v1", ResourceVersion: "foreign-rv"}
	if err := store.UpsertResource(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	var writer *snapshot.Writer
	if enabled {
		writer = snapshot.New(store, snapshot.Config{Workers: 2, QueueSize: 64}, nil)
		writer.Start(ctx)
		t.Cleanup(writer.Stop)
	}
	client := dynamicHistoryClient(t)
	tracker := operations.NewCoverageTracker(time.Minute)
	var synced atomic.Int64
	type delivery struct{ source, kind, version string }
	var mu sync.Mutex
	seen := map[delivery]bool{}
	observe := func(source string, r graph.Resource) {
		if r.ClusterID != "" {
			t.Error("standalone collector supplied a named cluster")
		}
		mu.Lock()
		defer mu.Unlock()
		seen[delivery{source, r.Kind, r.ResourceVersion}] = true
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
	delivered := func(wants ...delivery) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, want := range wants {
			if !seen[want] {
				return false
			}
		}
		return true
	}
	events := func() []graph.ResourceEvent {
		t.Helper()
		got, err := store.ListEvents(ctx, "", time.Time{}, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	start := func() func() {
		child, cancel := context.WithCancel(ctx)
		core, custom, constraints := standaloneInformerComponents([]discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{memberPods, memberConstraints}), discovery.WithOnSynced(func() { synced.Add(1) }), discovery.WithExtractor(probe("core"))}, client, memberMetadata{}, store, tracker, memberHistoryRegoFunc(func(_ context.Context, r graph.Resource) ([]graph.Edge, error) { observe("crd", r); return nil, nil }), probe("gatekeeper"), discovery.NewDynamicMetrics(), writer)
		results := make(chan error, 3)
		for _, component := range []componentStarter{core, custom, constraints} {
			go func() { results <- component.Start(child) }()
		}
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				for range 3 {
					select {
					case err := <-results:
						if !errors.Is(err, context.Canceled) {
							t.Errorf("standalone collector exit: %v", err)
						}
					case <-time.After(4 * time.Second):
						t.Error("standalone collector did not join")
					}
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	stop := start()
	awaitMember(t, "standalone initial callbacks", func() bool {
		return synced.Load() == 1 && delivered(delivery{"core", "Pod", "1"}, delivery{"core", "K8sRequiredLabels", "1"}, delivery{"crd", "Foo", "1"}, delivery{"crd", "K8sRequiredLabels", "1"}, delivery{"gatekeeper", "K8sRequiredLabels", "1"})
	})
	awaitMember(t, "existing standalone watch registrations", func() bool {
		count := 0
		for _, action := range client.Actions() {
			if action.GetVerb() == "watch" {
				count++
			}
		}
		return count >= 7
	})
	// Repair may add one initial fresh list per active registration; history
	// adds no watches or independent repair loop. All requests remain read-only.
	lists := map[schema.GroupVersionResource]int{}
	watches := map[schema.GroupVersionResource]int{}
	for _, action := range client.Actions() {
		switch action.GetVerb() {
		case "list":
			lists[action.GetResource()]++
		case "watch":
			watches[action.GetResource()]++
		default:
			t.Fatalf("history issued an unexpected Kubernetes action: %s", action.GetVerb())
		}
	}
	for gvr, want := range map[schema.GroupVersionResource]int{memberPods: 1, memberFoos: 1, memberConstraints: 3, memberCRDs: 1, memberTemplates: 1} {
		maxLists := want * 2
		if gvr == memberCRDs || gvr == memberTemplates {
			maxLists = want // Metadata discovery itself is not a repair endpoint.
		}
		if lists[gvr] < want || lists[gvr] > maxLists || watches[gvr] != want {
			t.Fatalf("%s list/watch calls=%d/%d want lists=%d..%d watches=%d", gvr, lists[gvr], watches[gvr], want, maxLists, want)
		}
	}
	if enabled {
		awaitMember(t, "three standalone initial events", func() bool { return len(events()) == 3 })
	} else if len(events()) != 0 {
		t.Fatal("disabled history produced events")
	}
	for _, gvr := range []schema.GroupVersionResource{memberFoos, memberConstraints} {
		ns := ""
		if gvr == memberFoos {
			ns = "default"
		}
		obj, err := client.Resource(gvr).Namespace(ns).Get(ctx, "same", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		obj.SetResourceVersion("2")
		obj.Object["spec"] = map[string]any{"private": "standalone-history-payload-canary"}
		if _, err := client.Resource(gvr).Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	awaitMember(t, "every standalone update source", func() bool {
		return delivered(delivery{"core", "K8sRequiredLabels", "2"}, delivery{"crd", "Foo", "2"}, delivery{"crd", "K8sRequiredLabels", "2"}, delivery{"gatekeeper", "K8sRequiredLabels", "2"})
	})
	if err := client.Resource(memberConstraints).Delete(ctx, "same", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitMember(t, "standalone constraint delete", func() bool {
		_, err := store.GetResource(ctx, "/K8sRequiredLabels/same")
		return errors.As(err, new(graph.ErrNotFound))
	})
	stop()
	// Recreate the production components against the retained graph receipts.
	// Probe state is test-only, and must not acknowledge the fresh callbacks.
	mu.Lock()
	seen = map[delivery]bool{}
	mu.Unlock()
	stop = start()
	awaitMember(t, "standalone replay after restart", func() bool {
		sources := memberSources(t, tracker, "")
		return synced.Load() == 2 && delivered(delivery{"core", "Pod", "1"}, delivery{"crd", "Foo", "2"}) && sources[operations.CoverageSourceCRD].State == operations.CoverageObserved && sources[operations.CoverageSourceGatekeeper].State == operations.CoverageObserved
	})
	stop()
	if writer != nil {
		writer.Stop()
	}
	got := events()
	want := 0
	if enabled {
		want = 6
	}
	if len(got) != want {
		t.Fatalf("standalone history events=%d want=%d: %+v", len(got), want, got)
	}
	unique := map[string]bool{}
	for _, e := range got {
		key := e.Kind + "/" + e.Name + "/" + string(e.EventType) + "/" + e.ResourceVersion
		if unique[key] || e.ClusterID != "" || e.Data != nil {
			t.Fatal("duplicate, payload or named-scope history", e)
		}
		unique[key] = true
	}
	if r, err := store.GetResource(ctx, foreign.ID()); err != nil || !graph.SameResourceVersion(r, foreign) {
		t.Fatal("standalone callbacks modified a same-UID member row", err)
	}
	if r, err := store.GetResource(ctx, "default/Foo/same"); err != nil || r.ResourceVersion != "2" {
		t.Fatal("standalone graph update missing", err)
	}
	page, err := store.ReadHistoryEvents(ctx, graph.HistoryEventQuery{ClusterID: "", From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Minute)})
	if err != nil || page.Truncated || len(page.Events) != want {
		t.Fatal("standalone metadata is not available to the scoped reader", err)
	}
}

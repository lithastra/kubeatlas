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
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func startStandaloneFixture(t *testing.T, core, custom, constraints componentStarter) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan error, 3)
	for _, component := range []componentStarter{core, custom, constraints} {
		go func() { results <- component.Start(ctx) }()
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

func TestStandaloneFactoryRepairsAllSourcesWithAndWithoutHistory(t *testing.T) {
	for _, mode := range []string{"disabled", "enabled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := memory.New()
			var writer *snapshot.Writer
			if mode == "enabled" {
				writer = snapshot.New(s, snapshot.Config{Workers: 2, QueueSize: 64}, nil)
				writer.Start(ctx)
				t.Cleanup(writer.Stop)
			}
			retained := []graph.Resource{
				{Kind: "Pod", Namespace: "default", Name: "missed", GroupVersion: "v1", UID: "old-pod", ResourceVersion: "1"},
				{Kind: "Foo", Namespace: "default", Name: "missed", GroupVersion: "example.com/v1", UID: "old-foo", ResourceVersion: "1"},
				{Kind: "K8sRequiredLabels", Name: "missed", GroupVersion: "constraints.gatekeeper.sh/v1beta1", UID: "old-constraint", ResourceVersion: "1"},
			}
			for _, row := range retained {
				for _, cluster := range []string{"", "east"} {
					row.ClusterID = cluster
					if err := s.UpsertResource(ctx, row); err != nil {
						t.Fatal(err)
					}
				}
				row.ClusterID = ""
				if err := s.UpsertEdge(ctx, graph.Edge{From: row.ID(), To: "default/Pod/same", Type: graph.EdgeTypeOwns}); err != nil {
					t.Fatal(err)
				}
			}
			oldAPI, retired, invalid := retained[1], retained[1], retained[1]
			oldAPI.Name, oldAPI.GroupVersion = "old-api", "example.com/v0"
			retired.Kind = "RetiredKind"
			invalid.Name, invalid.UID = "unversioned", ""
			for _, row := range []graph.Resource{oldAPI, retired, invalid} {
				if err := s.UpsertResource(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			client := dynamicHistoryClient(t)
			tracker := operations.NewCoverageTracker(time.Minute)
			var coreSynced, crdDelivered, gatekeeperDelivered atomic.Bool
			start := func() func() {
				coreSynced.Store(false)
				crdDelivered.Store(false)
				gatekeeperDelivered.Store(false)
				rules := memberHistoryRegoFunc(func(_ context.Context, r graph.Resource) ([]graph.Edge, error) {
					if r.Kind == "Foo" {
						crdDelivered.Store(true)
					}
					return nil, nil
				})
				probe := memberHistoryExtractorFunc(func(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
					edges, err := extractor.Default().ExtractAll(ctx, r, q)
					if err == nil && r.Kind == "K8sRequiredLabels" {
						gatekeeperDelivered.Store(true)
					}
					return edges, err
				})
				core, custom, constraints := standaloneInformerComponents([]discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{memberPods, memberConstraints}), discovery.WithOnSynced(func() { coreSynced.Store(true) })}, client, memberMetadata{}, s, tracker, rules, probe, discovery.NewDynamicMetrics(), writer)
				return startStandaloneFixture(t, core, custom, constraints)
			}
			stop := start()
			awaitMember(t, "standalone repairs across overlapping collectors", func() bool {
				for _, row := range retained {
					if _, err := s.GetResource(ctx, row.ID()); !errors.As(err, new(graph.ErrNotFound)) {
						return false
					}
				}
				return true
			})
			stop()
			// Recreate collectors against retained graph receipts. Missing rows
			// cannot produce another committed delete or duplicate history event.
			stop = start()
			awaitMember(t, "standalone restart initial delivery", func() bool {
				// The unversioned fixture deliberately keeps source coverage
				// partial; successful replay must not erase that gap.
				return coreSynced.Load() && crdDelivered.Load() && gatekeeperDelivered.Load()
			})
			stop()
			if writer != nil {
				writer.Stop()
			}
			for _, row := range retained {
				row.ClusterID = "east"
				if got, err := s.GetResource(ctx, row.ID()); err != nil || !graph.SameResourceVersion(got, row) {
					t.Fatal("standalone repair crossed a named-member boundary", got, err)
				}
			}
			for _, row := range []graph.Resource{oldAPI, retired, invalid} {
				if _, err := s.GetResource(ctx, row.ID()); err != nil {
					t.Fatal("unobserved or unversioned row removed", row, err)
				}
			}
			edges, err := s.ListEdges(ctx, "default/Pod/same", graph.DirectionIncoming)
			if err != nil || len(edges) != 0 {
				t.Fatal("missed-delete edges retained", edges, err)
			}
			raw, err := s.ListEvents(ctx, "", time.Time{}, time.Now().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range raw {
				if event.ClusterID != "" || event.Data != nil {
					t.Fatal("foreign or payload-bearing history event", event)
				}
			}
			page, err := s.ReadHistoryEvents(ctx, graph.HistoryEventQuery{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Minute)})
			if err != nil || page.Truncated {
				t.Fatal("history read failed", err)
			}
			deletes := map[string]int{}
			for _, event := range page.Events {
				if event.ClusterID != "" {
					t.Fatal("foreign history event", event)
				}
				if event.EventType == graph.EventTypeDelete {
					deletes[event.Kind]++
					if event.Name != "missed" {
						t.Fatal("unexpected repair event", event)
					}
				}
			}
			if mode == "disabled" && len(page.Events) != 0 {
				t.Fatal("graph repair enabled history implicitly")
			}
			if mode == "enabled" {
				for _, row := range retained {
					if deletes[row.Kind] != 1 {
						t.Fatal("repair history missing or duplicated", deletes)
					}
				}
			}
			for _, action := range client.Actions() {
				if action.GetResource().Resource == "secrets" || (action.GetVerb() != "list" && action.GetVerb() != "watch") {
					t.Fatal("unexpected standalone Kubernetes action", action)
				}
			}
		})
	}
}

func TestStandaloneCollectorsJoinBlockedRepairOnShutdown(t *testing.T) {
	s := &blockedMemberRepairStore{Store: memory.New(), entered: make(chan struct{}), release: make(chan struct{})}
	row := graph.Resource{Kind: "Pod", Namespace: "default", Name: "missed", GroupVersion: "v1", UID: "old-pod", ResourceVersion: "1"}
	if err := s.UpsertResource(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	tracker := operations.NewCoverageTracker(time.Minute)
	core, custom, constraints := standaloneInformerComponents([]discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{memberPods})}, memberDynamicClient(t), memberMetadata{}, s, tracker, nil, extractor.Default(), discovery.NewDynamicMetrics(), nil)
	stop := startStandaloneFixture(t, core, custom, constraints)
	var once sync.Once
	unblock := func() { once.Do(func() { close(s.release) }) }
	t.Cleanup(unblock)
	select {
	case <-s.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("repair did not reach conditional delete")
	}
	done := make(chan struct{})
	go func() { defer close(done); stop() }()
	select {
	case <-done:
		t.Fatal("collectors returned while repair still owned a mutation")
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("collectors did not join repair")
	}
	if _, err := s.GetResource(context.Background(), row.ID()); err != nil {
		t.Fatal("cancelled repair removed retained row", err)
	}
}

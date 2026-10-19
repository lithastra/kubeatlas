// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package crd

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type memberRules struct{ foreign bool }

func (r memberRules) EvaluateForResource(_ context.Context, res graph.Resource) ([]graph.Edge, error) {
	target := "default/Bar/target"
	if r.foreign {
		target = "west:" + target
	}
	return []graph.Edge{{From: res.Namespace + "/" + res.Kind + "/" + res.Name, To: target, Type: graph.EdgeTypeUsesConfigMap}}, nil
}
func memberCRDSource(t *testing.T, tracker *operations.CoverageTracker, cluster string) operations.CoverageSourceSnapshot {
	t.Helper()
	s, err := tracker.SnapshotCoverage(context.Background(), cluster, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range s.Sources {
		if source.Source == operations.CoverageSourceCRD {
			return source
		}
	}
	return operations.CoverageSourceSnapshot{}
}
func TestCRDMemberIdentityEdgesCoverageAndDeleteIsolation(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	store := memory.New()
	east, _ := coverageClient(t)
	west, _ := coverageClient(t)
	startCoverageDiscovery(t, New(east, store, WithCoverage(tracker), WithClusterID("east"), WithRegoEvaluator(memberRules{})))
	startCoverageDiscovery(t, New(west, store, WithCoverage(tracker), WithClusterID("west"), WithRegoEvaluator(memberRules{})))
	waitFor(t, 3*time.Second, "independent member CRD sources", func() bool {
		return memberCRDSource(t, tracker, "east").State == operations.CoverageObserved && memberCRDSource(t, tracker, "west").State == operations.CoverageObserved
	})
	for _, cluster := range []string{"east", "west"} {
		id := cluster + ":default/Foo/demo"
		resource, err := store.GetResource(context.Background(), id)
		if err != nil || resource.ClusterID != cluster {
			t.Fatalf("wrong member identity: %+v %v", resource, err)
		}
		edges, err := store.ListEdges(context.Background(), id, graph.DirectionOutgoing)
		if err != nil || len(edges) != 1 || edges[0].From != id || edges[0].To != cluster+":default/Bar/target" {
			t.Fatalf("wrong member rule edges: %+v %v", edges, err)
		}
	}
	if _, err := store.GetResource(context.Background(), "default/Foo/demo"); err == nil {
		t.Fatal("member resource stored under standalone identity")
	}
	if source := memberCRDSource(t, tracker, ""); source.Source != "" {
		t.Fatal("member source evidence leaked into standalone scope")
	}
	if err := east.Resource(coverageFooGVR).Namespace("default").Delete(context.Background(), "demo", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "east delete", func() bool {
		_, err := store.GetResource(context.Background(), "east:default/Foo/demo")
		return err != nil
	})
	if _, err := store.GetResource(context.Background(), "west:default/Foo/demo"); err != nil {
		t.Fatal("delete crossed member scope")
	}
	for _, action := range append(east.Actions(), west.Actions()...) {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("CRD collector read Secret objects")
		}
	}
}
func TestCRDMemberForeignRuleEdgeLeavesExplicitGap(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	client, _ := coverageClient(t)
	store := memory.New()
	startCoverageDiscovery(t, New(client, store, WithCoverage(tracker), WithClusterID("east"), WithRegoEvaluator(memberRules{foreign: true})))
	waitFor(t, 3*time.Second, "foreign edge gap", func() bool {
		return slices.Contains(coverageRow(memberCRDSource(t, tracker, "east"), coverageFooGVR).Reasons, operations.CoverageExtractionFailed)
	})
	edges, err := store.ListEdges(context.Background(), "east:default/Foo/demo", graph.DirectionOutgoing)
	if err != nil || len(edges) != 0 {
		t.Fatal("foreign rule edge persisted")
	}
}
func TestCRDMemberShutdownJoinsBlockedCallback(t *testing.T) {
	client, meta := coverageClient(t)
	store := &coverageBlockingStore{GraphStore: memory.New(), entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(store.release) })
	d := New(client, store, WithClusterID("east"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	select {
	case <-store.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("callback missing")
	}
	// Retire the registration first: shutdown must also join runs no longer in the active map.
	d.onCRDDelete(ctx, meta)
	cancel()
	select {
	case <-done:
		t.Fatal("collector returned with an in-flight graph callback")
	case <-time.After(30 * time.Millisecond):
	}
	release.Do(func() { close(store.release) })
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not join released callback")
	}
}

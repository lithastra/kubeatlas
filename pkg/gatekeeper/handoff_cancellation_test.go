// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package gatekeeper

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// Deliberately ignore cancellation to exercise dependencies already in flight
// when a template owner is replaced. Committed writes cannot be rolled back.
type handoffStore struct {
	*memory.Store
	beforeResource func(graph.Resource)
	beforeEdge     func(graph.Edge)
}

func (s *handoffStore) UpsertResource(_ context.Context, r graph.Resource) error {
	if s.beforeResource != nil {
		s.beforeResource(r)
	}
	return s.Store.UpsertResource(context.Background(), r)
}

func (s *handoffStore) UpsertEdge(_ context.Context, e graph.Edge) error {
	if s.beforeEdge != nil {
		s.beforeEdge(e)
	}
	return s.Store.UpsertEdge(context.Background(), e)
}

func TestTemplateHandoffStopsRetiredCallbackWork(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, transition := range []string{"replacement", "retirement", "shutdown"} {
				for _, stage := range []string{"resource", "extraction", "edge"} {
					t.Run(fmt.Sprintf("cluster=%s/history=%t/%s/stage=%s", cluster, history, transition, stage), func(t *testing.T) {
						testTemplateHandoff(t, cluster, history, transition, stage)
					})
				}
			}
		}
	}
}

func testTemplateHandoff(t *testing.T, cluster string, history bool, transition, stage string) {
	t.Helper()
	client, template := coverageClient()
	_, object := coverageObjects()
	object.SetUID("constraint-uid")
	if err := client.Tracker().Update(coverageConstraintGVR, object, ""); err != nil {
		t.Fatal(err)
	}
	tracker := operations.NewCoverageTracker(time.Minute)
	source, _ := tracker.BeginSource(cluster, operations.CoverageSourceGatekeeper, time.Now())
	meta, _ := source.BeginType(constraintTemplateGVR, time.Now())
	store := &handoffStore{Store: memory.New()}
	entered, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	releaseDependency := func() { released.Do(func() { close(release) }) }
	defer releaseDependency() // Also release on a failed assertion.
	block := func() { close(entered); <-release }
	var stale, current []graph.Edge
	resource := discovery.UnstructuredToResource(object, "K8sRequiredLabels")
	resource.ClusterID = cluster
	for _, name := range []string{"stale-first", "stale-second", "current"} {
		target := graph.Resource{ClusterID: cluster, Namespace: "default", Kind: "Pod", Name: name}
		if err := store.Store.UpsertResource(context.Background(), target); err != nil {
			t.Fatal(err)
		}
		edge := graph.Edge{From: resource.ID(), To: target.ID(), Type: graph.EdgeTypeEnforces}
		if name == "current" {
			current = append(current, edge)
		} else {
			stale = append(stale, edge)
		}
	}
	store.beforeResource = func(r graph.Resource) {
		if stage == "resource" && r.Origin.OwnerUID == "old-owner" {
			block()
		}
	}
	store.beforeEdge = func(e graph.Edge) {
		if stage == "edge" && e.To == stale[0].To {
			block()
		}
	}
	extractions := make(chan string, 4)
	ext := coverageExtractFunc(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
		extractions <- r.Origin.OwnerUID
		if r.Origin.OwnerUID == "old-owner" {
			if stage == "extraction" {
				block()
			}
			return stale, nil
		}
		return current, nil
	})
	manager := discovery.NewDynamicInformerManager(client)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = manager.Start(ctx) }()
	d := New(client, store, ext, manager, WithClusterID(cluster), WithMemberRepair(discovery.NewMemberRepair(nil, store, cluster)))
	d.coverageSession, d.metaCoverage = source, meta
	sink := &dynamicHistorySink{}
	if history {
		d.resourceHistory = discovery.NewResourceHistory(store, cluster, sink)
		if cluster == "" {
			d.resourceHistory = discovery.NewStandaloneResourceHistory(store, sink)
		}
	}
	// Cleanup runs after the deferred release, including on a failed assertion.
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("manager did not join callbacks")
		}
		d.repairs.Wait()
	})
	waitFor(t, 3*time.Second, "manager startup", manager.Started)
	template.SetUID("old-owner")
	d.onTemplate(ctx, template)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("old callback did not enter dependency")
	}
	switch transition {
	case "replacement":
		replacement := template.DeepCopy()
		replacement.SetUID("new-owner")
		d.onTemplate(ctx, replacement)
	case "retirement":
		d.onTemplateDelete(template)
	case "shutdown":
		cancel()
	}
	// Cancellation precedes release; an already-started write can still commit.
	releaseDependency()
	if transition == "replacement" {
		waitFor(t, 3*time.Second, "replacement callback completed", func() bool {
			edges, err := store.ListEdges(ctx, resource.ID(), graph.DirectionOutgoing)
			if err != nil {
				return false
			}
			for _, e := range edges {
				if e.To == current[0].To {
					return true
				}
			}
			return false
		})
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not drain callbacks")
	}
	d.repairs.Wait()
	saved, err := store.GetResource(context.Background(), resource.ID())
	wantOwner := "old-owner"
	want := 0
	if transition == "replacement" {
		wantOwner, want = "new-owner", 1
	}
	if err != nil || saved.Origin.OwnerUID != wantOwner {
		t.Fatalf("handoff lost committed resource/receipt: %+v %v", saved.Origin, err)
	}
	capture, err := tracker.SnapshotCoverage(context.Background(), cluster, nil)
	if err != nil || graph.OriginQualification(saved, capture.Origins) != "retired" {
		t.Fatalf("shutdown requalified retained data: %+v %v", capture.Origins, err)
	}
	edges, err := store.ListEdges(context.Background(), resource.ID(), graph.DirectionOutgoing)
	if stage == "edge" {
		want++ // The already in-flight edge can commit; no compensating cleanup.
	}
	if err != nil || len(edges) != want {
		t.Fatalf("retired callback continued its edge batch: %+v %v", edges, err)
	}
	for _, e := range edges {
		if e.To == stale[1].To || (stage != "edge" && e.To == stale[0].To) {
			t.Fatalf("new work began after retirement: %+v", e)
		}
	}
	if stage == "resource" {
		for len(extractions) > 0 {
			if <-extractions == "old-owner" {
				t.Fatal("retired resource write continued into extraction")
			}
		}
	}
	if history && len(sink.events) != 1 {
		t.Fatalf("owner handoff manufactured object history: %+v", sink.events)
	}
}

type canceledDependencyStore struct {
	*memory.Store
	cancel context.CancelFunc
	stage  string
}

func (s *canceledDependencyStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	if s.stage == "resource" {
		s.cancel()
		return ctx.Err()
	}
	return s.Store.UpsertResource(ctx, r)
}

func (s *canceledDependencyStore) UpsertEdge(ctx context.Context, e graph.Edge) error {
	s.cancel()
	return ctx.Err()
}

func (s *canceledDependencyStore) DeleteResource(ctx context.Context, _ string) error {
	s.cancel()
	return ctx.Err()
}

func (s *canceledDependencyStore) DeleteResourceVersion(ctx context.Context, _ graph.Resource) (bool, error) {
	s.cancel()
	return false, ctx.Err()
}

func TestConstraintCancellationPreservesExistingGaps(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, stage := range []string{"resource", "extraction", "edge", "delete"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/stage=%s", cluster, history, stage), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					store := &canceledDependencyStore{Store: memory.New(), cancel: cancel, stage: stage}
					tracker := operations.NewCoverageTracker(time.Minute)
					source, _ := tracker.BeginSource(cluster, operations.CoverageSourceGatekeeper, time.Now())
					token, _ := source.BeginType(coverageConstraintGVR, time.Now())
					existingGap := time.Now().Add(-time.Minute)
					token.RecordGap(coverageConstraintGVR, operations.CoverageProcessingFailed, existingGap)
					_, object := coverageObjects()
					object.SetUID("constraint-uid")
					r := discovery.UnstructuredToResource(object, "K8sRequiredLabels")
					r.ClusterID = cluster
					if stage == "delete" {
						if err := store.Store.UpsertResource(ctx, r); err != nil {
							t.Fatal(err)
						}
					}
					ext := coverageExtractFunc(func(ctx context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
						if stage == "extraction" {
							cancel()
							return nil, ctx.Err()
						}
						return []graph.Edge{{From: r.ID(), To: r.ID(), Type: graph.EdgeTypeEnforces}}, nil
					})
					d := New(nil, store, ext, nil, WithClusterID(cluster), WithMemberRepair(discovery.NewMemberRepair(nil, store, cluster)))
					sink := &dynamicHistorySink{}
					if history {
						d.resourceHistory = discovery.NewResourceHistory(store, cluster, sink)
						if cluster == "" {
							d.resourceHistory = discovery.NewStandaloneResourceHistory(store, sink)
						}
					}
					if stage == "delete" {
						d.handleConstraintDelete(ctx, r.Kind, object, token)
					} else {
						d.handleConstraint(ctx, r.Kind, object, token)
					}
					if ctx.Err() == nil {
						t.Fatal("dependency did not cancel the callback")
					}
					capture, err := tracker.SnapshotCoverage(context.Background(), cluster, nil)
					if err != nil {
						t.Fatal(err)
					}
					row := coverageRow(capture.Sources[0], coverageConstraintGVR)
					if !row.ProcessingGapSince.Equal(existingGap) || !row.PersistenceGapSince.IsZero() || !row.ExtractionGapSince.IsZero() {
						t.Fatalf("cancellation changed existing loss evidence: %+v", row)
					}
					edges, err := store.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
					if err != nil || len(edges) != 0 {
						t.Fatalf("canceled dependency committed an edge: %+v %v", edges, err)
					}
					if stage == "delete" {
						if _, err := store.GetResource(context.Background(), r.ID()); err != nil {
							t.Fatal("canceled delete lost retained resource", err)
						}
					}
					wantEvents := 0
					if history && (stage == "extraction" || stage == "edge") {
						wantEvents = 1 // The resource committed before cancellation.
					}
					if len(sink.events) != wantEvents {
						t.Fatalf("canceled operation changed committed history: %+v", sink.events)
					}
				})
			}
		}
	}
}

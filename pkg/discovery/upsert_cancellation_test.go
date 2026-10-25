// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// A store call already in flight can ignore cancellation and still commit.
// These hooks exercise the boundary between that commit and subsequent work.
type canceledUpsertStore struct {
	*memory.Store
	resourceHook func(context.Context, graph.Resource) error
	edgeHook     func(context.Context, graph.Edge) error
}

func (s *canceledUpsertStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	if s.resourceHook != nil {
		if err := s.resourceHook(ctx, r); err != nil {
			return err
		}
	}
	return s.Store.UpsertResource(context.Background(), r)
}

func (s *canceledUpsertStore) UpsertEdge(ctx context.Context, e graph.Edge) error {
	if s.edgeHook != nil {
		if err := s.edgeHook(ctx, e); err != nil {
			return err
		}
	}
	return s.Store.UpsertEdge(context.Background(), e)
}

var cancellationPods = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func cancellationEdges(r graph.Resource) []graph.Edge {
	first := graph.Resource{ClusterID: r.ClusterID, Kind: "Secret", Namespace: r.Namespace, Name: "first"}
	second := first
	second.Name = "second"
	return []graph.Edge{
		{From: r.ID(), To: first.ID(), Type: graph.EdgeTypeUsesSecret},
		{From: r.ID(), To: second.ID(), Type: graph.EdgeTypeUsesSecret},
	}
}

func cancellationHistory(m *InformerManager, s graph.GraphStore, cluster string, sink *resyncSink) {
	m.snapshotSink = sink
	m.resourceHistory = NewResourceHistory(s, cluster, sink)
	if cluster == "" {
		m.resourceHistory = NewStandaloneResourceHistory(s, sink)
	}
}

func TestCoreShutdownStopsSubsequentUpsertStages(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, replay := range []bool{false, true} {
				for _, stage := range []string{"resource", "extraction", "secret", "edge"} {
					if replay && stage == "resource" {
						continue // Unchanged replay deliberately skips resource persistence.
					}
					t.Run(fmt.Sprintf("cluster=%s/history=%t/replay=%t/stage=%s", cluster, history, replay, stage), func(t *testing.T) {
						testCoreUpsertCancellation(t, cluster, history, replay, stage)
					})
				}
			}
		}
	}
}

func testCoreUpsertCancellation(t *testing.T, cluster string, history, replay bool, stage string) {
	t.Helper()
	object := resyncObject("Pod", "blocked", "object-uid", "version-one")
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{cancellationPods: "PodList"}, object)
	s := &canceledUpsertStore{Store: memory.New()}
	tracker := operations.NewCoverageTracker(time.Minute)
	sink := &resyncSink{}
	var notifications, extractions atomic.Int64
	repair := NewMemberRepair(client, s, cluster)
	if cluster == "" {
		repair = NewStandaloneRepair(client, s)
	}
	m := NewInformerManager(client, s, WithClusterID(cluster), WithCoverage(tracker), WithGVRs([]schema.GroupVersionResource{cancellationPods}), WithMemberRepair(repair),
		WithBroadcaster(func(event, _, _, _ string) {
			if event == "upsert" {
				notifications.Add(1)
			}
		}))
	if history {
		cancellationHistory(m, s, cluster, sink)
	}
	if replay {
		m.handleUpsert(context.Background(), cancellationPods, object, graph.EventTypeAdd)
		notifications.Store(0)
	}
	r := UnstructuredToResource(object, "Pod")
	r.ClusterID = cluster
	foreign := r
	foreign.ClusterID = "west"
	if err := s.Store.UpsertResource(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var blocked, released sync.Once
	releaseDependency := func() { released.Do(func() { close(release) }) }
	defer releaseDependency()
	block := func() { blocked.Do(func() { close(entered); <-release }) }
	s.resourceHook = func(_ context.Context, r graph.Resource) error {
		if (stage == "resource" && r.Kind == "Pod") || (stage == "secret" && r.Kind == "Secret") {
			block()
		}
		return nil
	}
	s.edgeHook = func(context.Context, graph.Edge) error {
		if stage == "edge" {
			block()
		}
		return nil
	}
	m.extractor = resyncExtractorFunc(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
		extractions.Add(1)
		if stage == "extraction" {
			block()
		}
		return cancellationEdges(r), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var startErr error
	go func() { defer close(done); startErr = m.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("core failed to join blocked callback")
		}
	})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not enter selected dependency")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("core shutdown returned before its callback drained")
	case <-time.After(20 * time.Millisecond):
	}
	releaseDependency()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("core callback did not drain after dependency returned")
	}
	if !errors.Is(startErr, context.Canceled) {
		t.Fatal("unexpected core exit", startErr)
	}
	if got, err := s.GetResource(context.Background(), r.ID()); err != nil || !graph.SameResourceVersion(got, r) {
		t.Fatal("shutdown lost committed resource", got, err)
	}
	if got, err := s.GetResource(context.Background(), foreign.ID()); err != nil || !graph.SameResourceVersion(got, foreign) {
		t.Fatal("callback crossed member scope", got, err)
	}
	if stage == "resource" && extractions.Load() != 0 {
		t.Fatal("retired resource write continued into extraction")
	}
	wantRefs := 0
	switch stage {
	case "secret":
		wantRefs = 1 // The first placeholder was already in flight.
	case "edge":
		wantRefs = 2 // Both placeholders committed before the edge call began.
	}
	for i, name := range []string{"first", "second"} {
		ref := graph.Resource{ClusterID: cluster, Namespace: r.Namespace, Kind: "Secret", Name: name}
		got, err := s.GetResource(context.Background(), ref.ID())
		if i >= wantRefs {
			if !errors.As(err, new(graph.ErrNotFound)) {
				t.Fatal("new Secret placeholder write began after cancellation", got, err)
			}
		} else if err != nil || got.Raw != nil || got.UID != "" || got.ResourceVersion != "" || len(got.Labels) != 0 || len(got.Annotations) != 1 || got.Annotations[graph.ReferenceOnlyAnnotation] != "true" {
			t.Fatal("committed placeholder exceeded reference-only boundary", got, err)
		}
	}
	edges, err := s.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing)
	wantEdges := 0
	if stage == "edge" {
		wantEdges = 1 // Only the already-started edge may commit.
	}
	if err != nil || len(edges) != wantEdges {
		t.Fatal("shutdown continued the edge batch", edges, err)
	}
	wantNotifications := int64(1)
	if replay && stage == "extraction" {
		wantNotifications = 0 // No graph write occurred in this callback.
	}
	if notifications.Load() != wantNotifications {
		t.Fatalf("confirmed writes lost/duplicated notification: %d want %d", notifications.Load(), wantNotifications)
	}
	wantEvents := 0
	if history {
		wantEvents = 1
	}
	if events := sink.recorded(); len(events) != wantEvents {
		t.Fatal("shutdown changed committed resource history", events)
	}
	for _, action := range client.Actions() {
		if action.GetResource() != cancellationPods || (action.GetVerb() != "list" && action.GetVerb() != "watch") {
			t.Fatal("callback introduced a non-pod/Secret/mutating request", action)
		}
	}
}

func TestCoreCanceledUpsertPreservesExistingGaps(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, stage := range []string{"entry", "resource", "extraction", "secret", "edge"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/stage=%s", cluster, history, stage), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					s := &canceledUpsertStore{Store: memory.New()}
					tracker := operations.NewCoverageTracker(time.Minute)
					token, err := tracker.Begin(cluster, []schema.GroupVersionResource{cancellationPods}, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					gap := time.Now().Add(-time.Minute)
					token.InitialDeliveryComplete(cancellationPods)
					token.WatchStarted(cancellationPods)
					token.RecordGap(cancellationPods, operations.CoverageProcessingFailed, gap)
					var notifications, extractions atomic.Int64
					sink := &resyncSink{}
					m := NewInformerManager(nil, s, WithClusterID(cluster), WithMemberRepair(NewMemberRepair(nil, s, cluster)), WithBroadcaster(func(_, _, _, _ string) { notifications.Add(1) }))
					m.coverageSession = token
					if history {
						cancellationHistory(m, s, cluster, sink)
					}
					s.resourceHook = func(ctx context.Context, r graph.Resource) error {
						if (stage == "resource" && r.Kind == "Pod") || (stage == "secret" && r.Kind == "Secret") {
							cancel()
							return ctx.Err()
						}
						return nil
					}
					s.edgeHook = func(ctx context.Context, _ graph.Edge) error {
						cancel()
						return ctx.Err()
					}
					m.extractor = resyncExtractorFunc(func(ctx context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
						extractions.Add(1)
						if stage == "extraction" {
							cancel()
							return cancellationEdges(r), ctx.Err()
						}
						return cancellationEdges(r), nil
					})
					object := resyncObject("Pod", "canceled", "uid", "version")
					if stage == "entry" {
						cancel()
						m.handleUpsert(ctx, cancellationPods, "invalid canceled input", graph.EventTypeAdd)
					} else {
						m.handleUpsert(ctx, cancellationPods, object, graph.EventTypeAdd)
					}
					if ctx.Err() == nil {
						t.Fatal("selected dependency did not cancel")
					}
					capture, err := tracker.SnapshotCoverage(context.Background(), cluster, []schema.GroupVersionResource{cancellationPods})
					if err != nil {
						t.Fatal(err)
					}
					row := capture.Resources[0]
					if !row.ProcessingGapSince.Equal(gap) || !row.PersistenceGapSince.IsZero() || !row.ExtractionGapSince.IsZero() {
						t.Fatal("cancellation changed prior gap evidence", row)
					}
					wantNotifications, wantEvents := int64(1), 0
					if history {
						wantEvents = 1
					}
					if stage == "entry" || stage == "resource" {
						wantNotifications, wantEvents = 0, 0
						if extractions.Load() != 0 || len(m.persisted) != 0 {
							t.Fatal("failed/canceled write advanced the callback")
						}
					}
					if notifications.Load() != wantNotifications || len(sink.recorded()) != wantEvents {
						t.Fatal("uncommitted write notified or committed history was lost", notifications.Load(), sink.recorded())
					}
					r := UnstructuredToResource(object, "Pod")
					r.ClusterID = cluster
					if edges, err := s.ListEdges(context.Background(), r.ID(), graph.DirectionOutgoing); err != nil || len(edges) != 0 {
						t.Fatal("canceled work wrote an edge", edges, err)
					}
					for _, event := range sink.recorded() {
						if event.ClusterID != cluster || event.Data != nil || event.EventType != graph.EventTypeAdd {
							t.Fatal("commit history lost scope or retained payload", event)
						}
					}
				})
			}
		}
	}
}

func TestCoreUpsertNotifiesConfirmedWritesAndKeepsActivePartialExtraction(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/partial=%t", cluster, history, partial), func(t *testing.T) {
					ctx := context.Background()
					s := &canceledUpsertStore{Store: memory.New()}
					sink := &resyncSink{}
					var notifications atomic.Int64
					tracker := operations.NewCoverageTracker(time.Minute)
					token, err := tracker.Begin(cluster, []schema.GroupVersionResource{cancellationPods}, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					m := NewInformerManager(nil, s, WithClusterID(cluster), WithBroadcaster(func(_, _, _, _ string) { notifications.Add(1) }))
					m.coverageSession = token
					if history {
						cancellationHistory(m, s, cluster, sink)
					}
					object := resyncObject("Pod", "active", "uid", "version")
					m.handleUpsert(ctx, cancellationPods, object, graph.EventTypeAdd)
					notifications.Store(0)
					m.extractor = resyncExtractorFunc(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
						edges := []graph.Edge{{From: r.ID(), To: r.ID(), Type: graph.EdgeTypeOwns}}
						if partial {
							return edges, errors.New("active partial extraction")
						}
						return edges, nil
					})
					s.edgeHook = func(context.Context, graph.Edge) error {
						if !partial {
							return errors.New("failed replay edge write")
						}
						return nil
					}
					m.handleUpsert(ctx, cancellationPods, object, graph.EventTypeUpdate)
					wantNotifications, wantEdges := int64(0), 0
					if partial {
						wantNotifications, wantEdges = 1, 1
					}
					r := UnstructuredToResource(object, "Pod")
					r.ClusterID = cluster
					if edges, err := s.ListEdges(ctx, r.ID(), graph.DirectionOutgoing); err != nil || len(edges) != wantEdges || notifications.Load() != wantNotifications {
						t.Fatal("partial/failed extraction notified uncommitted work or lost committed edges", edges, notifications.Load(), err)
					}
					capture, err := tracker.SnapshotCoverage(ctx, cluster, []schema.GroupVersionResource{cancellationPods})
					if err != nil {
						t.Fatal(err)
					}
					row := capture.Resources[0]
					if (partial && row.ExtractionGapSince.IsZero()) || (!partial && row.PersistenceGapSince.IsZero()) {
						t.Fatal("active failure lost gap reporting", row)
					}
					if !partial {
						s.edgeHook = nil
						m.handleUpsert(ctx, cancellationPods, object, graph.EventTypeUpdate)
						if notifications.Load() != 1 {
							t.Fatal("later confirmed edge recovery did not notify")
						}
					}
					wantEvents := 0
					if history {
						wantEvents = 1
					}
					if len(sink.recorded()) != wantEvents {
						t.Fatal("edge replay manufactured resource history", sink.recorded())
					}
				})
			}
		}
	}
}

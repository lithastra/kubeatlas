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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

var configMapGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// Count attempts, including failures, so tests detect writes hidden by an
// idempotent store. All helpers also support real informer goroutines.
type resyncStore struct {
	graph.GraphStore
	resourceWrites atomic.Int64
	edgeWrites     atomic.Int64
	failResource   atomic.Bool
	failEdge       atomic.Bool
	failSecret     atomic.Bool
}

func (s *resyncStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	s.resourceWrites.Add(1)
	if s.failResource.Load() || (r.Kind == "Secret" && s.failSecret.Load()) {
		return errors.New("injected resource write failure")
	}
	return s.GraphStore.UpsertResource(ctx, r)
}

func (s *resyncStore) UpsertEdge(ctx context.Context, e graph.Edge) error {
	s.edgeWrites.Add(1)
	if s.failEdge.Load() {
		return errors.New("injected edge write failure")
	}
	return s.GraphStore.UpsertEdge(ctx, e)
}

type resyncSink struct {
	mu     sync.Mutex
	events []graph.ResourceEvent
}

func (s *resyncSink) Enqueue(e graph.ResourceEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *resyncSink) recorded() []graph.ResourceEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]graph.ResourceEvent(nil), s.events...)
}

type resyncExtractorFunc func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error)

func (f resyncExtractorFunc) ExtractAll(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	return f(ctx, r, q)
}

func resyncObject(kind, name, uid, rv string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind(kind)
	u.SetNamespace("test")
	u.SetName(name)
	u.SetUID(types.UID(uid))
	u.SetResourceVersion(rv)
	return u
}

func TestInformerResyncSkipsUnchangedResourceWrites(t *testing.T) {
	ctx := context.Background()
	store := &resyncStore{GraphStore: memory.New()}
	sink := &resyncSink{}
	var broadcasts atomic.Int64
	mgr := NewInformerManager(nil, store, WithExtractor(extractor.Default()), WithSnapshotSink(sink),
		WithBroadcaster(func(_, _, _, _ string) { broadcasts.Add(1) }))

	const count = 10_000
	objects := make([]*unstructured.Unstructured, count)
	for i := range objects {
		objects[i] = resyncObject("ConfigMap", fmt.Sprintf("fixture-%d", i), fmt.Sprintf("uid-%d", i), "1")
		mgr.handleUpsert(ctx, configMapGVR, objects[i], graph.EventTypeAdd)
	}
	for _, obj := range objects {
		mgr.handleUpsert(ctx, configMapGVR, obj.DeepCopy(), graph.EventTypeUpdate)
	}
	if got := store.resourceWrites.Load(); got != count {
		t.Fatalf("unchanged resync caused %d extra resource writes, want 0", got-count)
	}
	if got := len(sink.recorded()); got != count {
		t.Fatalf("resync produced duplicate history: got %d events, want %d", got, count)
	}
	if got := broadcasts.Load(); got != count {
		t.Fatalf("resync produced duplicate notifications: got %d, want %d", got, count)
	}

	updated := objects[0].DeepCopy()
	updated.SetResourceVersion("2")
	updated.SetLabels(map[string]string{"changed": "true"})
	mgr.handleUpsert(ctx, configMapGVR, updated, graph.EventTypeUpdate)
	got, err := store.GetResource(ctx, "test/ConfigMap/fixture-0")
	if err != nil || got.ResourceVersion != "2" || got.Labels["changed"] != "true" {
		t.Fatalf("real update was lost: resource=%+v, err=%v", got, err)
	}
	events := sink.recorded()
	if store.resourceWrites.Load() != count+1 || len(events) != count+1 || events[count].EventType != graph.EventTypeUpdate {
		t.Fatal("real update must produce exactly one new write and history event")
	}
}

func TestInformerResyncRetriesFailedResourceWrite(t *testing.T) {
	for _, initialFailure := range []bool{true, false} {
		t.Run(fmt.Sprintf("initial_failure=%t", initialFailure), func(t *testing.T) {
			ctx := context.Background()
			store := &resyncStore{GraphStore: memory.New()}
			sink := &resyncSink{}
			mgr := NewInformerManager(nil, store, WithSnapshotSink(sink))
			obj := resyncObject("ConfigMap", "canary", "uid", "1")
			if !initialFailure {
				mgr.handleUpsert(ctx, configMapGVR, obj, graph.EventTypeAdd)
				obj.SetResourceVersion("2")
			}
			before := len(sink.recorded())
			store.failResource.Store(true)
			mgr.handleUpsert(ctx, configMapGVR, obj, graph.EventTypeUpdate)
			if len(sink.recorded()) != before {
				t.Fatal("failed resource write recorded a history event")
			}
			store.failResource.Store(false)
			mgr.handleUpsert(ctx, configMapGVR, obj.DeepCopy(), graph.EventTypeUpdate)
			got, err := store.GetResource(ctx, "test/ConfigMap/canary")
			if err != nil || got.ResourceVersion != obj.GetResourceVersion() || len(sink.recorded()) != before+1 {
				t.Fatalf("same-version retry did not persist resource: resource=%+v, err=%v", got, err)
			}
			writes := store.resourceWrites.Load()
			mgr.handleUpsert(ctx, configMapGVR, obj, graph.EventTypeUpdate)
			if store.resourceWrites.Load() != writes || len(sink.recorded()) != before+1 {
				t.Fatal("successful retry was written again on the next resync")
			}
		})
	}
}

func TestInformerResyncReconcilesLateSelectorTarget(t *testing.T) {
	ctx := context.Background()
	store := &resyncStore{GraphStore: memory.New()}
	registry := &extractor.Registry{}
	registry.Register(extractor.SelectsExtractor{})
	var broadcasts int
	mgr := NewInformerManager(nil, store, WithExtractor(registry),
		WithBroadcaster(func(_, _, _, _ string) { broadcasts++ }))
	svc := resyncObject("Service", "api", "service-uid", "1")
	svc.Object["spec"] = map[string]any{"selector": map[string]any{"app": "api"}}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "services"}
	mgr.handleUpsert(ctx, gvr, svc, graph.EventTypeAdd)

	// The Service was processed before its Pod. Its own version never changes.
	pod := graph.Resource{Kind: "Pod", Namespace: "test", Name: "api", Labels: map[string]string{"app": "api"}}
	if err := store.GraphStore.UpsertResource(ctx, pod); err != nil {
		t.Fatal(err)
	}
	mgr.handleUpsert(ctx, gvr, svc.DeepCopy(), graph.EventTypeUpdate)
	edges, err := store.ListEdges(ctx, "test/Service/api", graph.DirectionOutgoing)
	if err != nil || len(edges) != 1 || edges[0].To != pod.ID() {
		t.Fatalf("unchanged Service did not discover late Pod: edges=%v, err=%v", edges, err)
	}
	if store.resourceWrites.Load() != 1 || broadcasts != 2 {
		t.Fatal("edge repair must notify clients without rewriting the unchanged Service")
	}
}

func TestInformerResyncRetriesEdgeReconciliation(t *testing.T) {
	for _, failure := range []string{"extractor", "edge", "secret-reference"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			store := &resyncStore{GraphStore: memory.New()}
			sink := &resyncSink{}
			failExtraction := failure == "extractor"
			store.failEdge.Store(failure == "edge")
			store.failSecret.Store(failure == "secret-reference")
			registry := resyncExtractorFunc(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
				if failExtraction {
					return nil, errors.New("injected extractor failure")
				}
				return []graph.Edge{{From: r.ID(), To: "test/Secret/database", Type: graph.EdgeTypeUsesSecret}}, nil
			})
			mgr := NewInformerManager(nil, store, WithExtractor(registry), WithSnapshotSink(sink))
			obj := resyncObject("Pod", "api", "uid", "1")
			gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
			mgr.handleUpsert(ctx, gvr, obj, graph.EventTypeAdd)
			failExtraction = false
			store.failEdge.Store(false)
			store.failSecret.Store(false)
			mgr.handleUpsert(ctx, gvr, obj.DeepCopy(), graph.EventTypeUpdate)
			edges, err := store.ListEdges(ctx, "test/Pod/api", graph.DirectionOutgoing)
			if err != nil || len(edges) != 1 {
				t.Fatalf("resync did not repair edge: edges=%v, err=%v", edges, err)
			}
			ref, err := store.GetResource(ctx, "test/Secret/database")
			if err != nil || ref.Raw != nil || ref.Annotations[graph.ReferenceOnlyAnnotation] != "true" {
				t.Fatalf("resync did not repair reference-only placeholder: err=%v", err)
			}
			if len(sink.recorded()) != 1 {
				t.Fatal("edge retry recorded a duplicate resource history event")
			}
		})
	}
}

func TestInformerResyncIdentityAndMissingVersions(t *testing.T) {
	ctx := context.Background()
	store := &resyncStore{GraphStore: memory.New()}
	mgr := NewInformerManager(nil, store)
	obj := resyncObject("ConfigMap", "canary", "first", "1")
	mgr.handleUpsert(ctx, configMapGVR, obj, graph.EventTypeAdd)
	// Same name and version do not identify the same object after recreation.
	obj.SetUID("second")
	mgr.handleUpsert(ctx, configMapGVR, obj, graph.EventTypeUpdate)
	if store.resourceWrites.Load() != 2 {
		t.Fatal("recreated object with different UID was ignored")
	}
	for _, field := range []string{"uid", "resourceVersion"} {
		unknown := obj.DeepCopy()
		unstructured.RemoveNestedField(unknown.Object, "metadata", field)
		before := store.resourceWrites.Load()
		mgr.handleUpsert(ctx, configMapGVR, unknown, graph.EventTypeUpdate)
		mgr.handleUpsert(ctx, configMapGVR, unknown, graph.EventTypeUpdate)
		mgr.handleUpsert(ctx, configMapGVR, obj, graph.EventTypeUpdate)
		if store.resourceWrites.Load() != before+3 {
			t.Fatalf("missing %s must disable deduplication and invalidate the previous marker", field)
		}
	}
}

func TestInformerResyncForgetsDeletedObjects(t *testing.T) {
	for _, tombstone := range []bool{false, true} {
		t.Run(fmt.Sprintf("tombstone=%t", tombstone), func(t *testing.T) {
			ctx := context.Background()
			store := &resyncStore{GraphStore: memory.New()}
			mgr := NewInformerManager(nil, store, WithClusterID("east"))
			obj := resyncObject("ConfigMap", "canary", "uid", "1")
			mgr.handleUpsert(ctx, configMapGVR, obj, graph.EventTypeAdd)
			deleted := obj.DeepCopy()
			deleted.SetKind("") // Resolve the same Kind as the upsert path.
			var event any = deleted
			if tombstone {
				event = cache.DeletedFinalStateUnknown{Key: "test/canary", Obj: deleted}
			}
			mgr.handleDelete(ctx, configMapGVR, event)
			if len(mgr.persisted) != 0 {
				t.Fatal("delete retained a version marker")
			}
			if _, err := store.GetResource(ctx, "east:test/ConfigMap/canary"); err == nil {
				t.Fatal("delete failed to use cached Kind and cluster identity")
			}
			mgr.handleUpsert(ctx, configMapGVR, obj, graph.EventTypeUpdate)
			if store.resourceWrites.Load() != 2 {
				t.Fatal("same-version delivery after deletion was incorrectly suppressed")
			}
		})
	}
}

func TestInformerResyncConcurrentGVRsAndClusters(t *testing.T) {
	store := &resyncStore{GraphStore: memory.New()}
	managers := []*InformerManager{
		NewInformerManager(nil, store, WithClusterID("east")),
		NewInformerManager(nil, store, WithClusterID("west")),
	}
	var wg sync.WaitGroup
	for _, mgr := range managers {
		for _, kind := range []string{"ConfigMap", "Pod"} {
			wg.Go(func() {
				gvr := configMapGVR
				if kind == "Pod" {
					gvr.Resource = "pods"
				}
				obj := resyncObject(kind, "same-name", "same-uid", "1")
				for range 100 {
					mgr.handleUpsert(context.Background(), gvr, obj, graph.EventTypeUpdate)
				}
			})
		}
	}
	wg.Wait()
	if store.resourceWrites.Load() != 4 {
		t.Fatalf("independent GVR/cluster identities need one write each, got %d", store.resourceWrites.Load())
	}
	for _, id := range []string{"east:test/ConfigMap/same-name", "east:test/Pod/same-name", "west:test/ConfigMap/same-name", "west:test/Pod/same-name"} {
		if _, err := store.GetResource(context.Background(), id); err != nil {
			t.Fatalf("missing %s: %v", id, err)
		}
	}
}

func TestInformerResyncWithDynamicClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	obj := resyncObject("ConfigMap", "canary", "uid", "1")
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{configMapGVR: "ConfigMapList"}, obj)
	store := &resyncStore{GraphStore: memory.New()}
	sink := &resyncSink{}
	var extractions atomic.Int64
	registry := resyncExtractorFunc(func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error) {
		extractions.Add(1)
		return nil, nil
	})
	mgr := NewInformerManager(client, store, WithGVRs([]schema.GroupVersionResource{configMapGVR}),
		WithResync(time.Second), WithSnapshotSink(sink), WithExtractor(registry))
	done := make(chan error, 1)
	go func() {
		err := mgr.Start(ctx)
		mgr.factory.Shutdown()
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Start shutdown: %v", err)
		}
	})
	await := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !condition() {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for fake-client informer")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	await(func() bool { return extractions.Load() >= 2 })
	if store.resourceWrites.Load() != 1 || len(sink.recorded()) != 1 {
		t.Fatal("actual informer resync duplicated resource writes or history")
	}
	updated := obj.DeepCopy()
	updated.SetResourceVersion("2")
	if _, err := client.Resource(configMapGVR).Namespace("test").Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	await(func() bool { return len(sink.recorded()) == 2 })
	if err := client.Resource(configMapGVR).Namespace("test").Delete(ctx, obj.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	await(func() bool { return len(sink.recorded()) == 3 })
	if _, err := store.GetResource(ctx, "test/ConfigMap/canary"); err == nil {
		t.Fatal("deleted resource remains in store")
	}
	if _, err := client.Resource(configMapGVR).Namespace("test").Create(ctx, updated, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	await(func() bool { return len(sink.recorded()) == 4 })
	if store.resourceWrites.Load() != 3 {
		t.Fatal("delete must clear the persisted marker before an object is added again")
	}
}

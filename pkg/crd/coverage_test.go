// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

var coverageFooGVR = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "foos"}

func coverageObjects(t *testing.T) (*unstructured.Unstructured, *unstructured.Unstructured) {
	t.Helper()
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(fooCRD())
	if err != nil {
		t.Fatal(err)
	}
	meta := &unstructured.Unstructured{Object: obj}
	meta.SetAPIVersion("apiextensions.k8s.io/v1")
	meta.SetKind("CustomResourceDefinition")
	meta.SetUID("synthetic-crd-uid")
	foo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1", "kind": "Foo", "metadata": map[string]any{"name": "demo", "namespace": "default", "resourceVersion": "1"},
		"spec": map[string]any{"value": "synthetic-payload-canary"},
	}}
	return meta, foo
}

func coverageClient(t *testing.T) (*dynamicfake.FakeDynamicClient, *unstructured.Unstructured) {
	t.Helper()
	meta, foo := coverageObjects(t)
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList", coverageFooGVR: "FooList"}, meta, foo), meta
}

func startCoverageDiscovery(t *testing.T, d *Discovery) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("collector exit: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("collector failed to stop")
		}
	})
	return ctx
}

func crdCoverage(t *testing.T, tracker *operations.CoverageTracker) operations.CoverageSourceSnapshot {
	t.Helper()
	out, err := tracker.SnapshotCoverage(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range out.Sources {
		if source.Source == operations.CoverageSourceCRD {
			return source
		}
	}
	return operations.CoverageSourceSnapshot{}
}

func coverageRow(source operations.CoverageSourceSnapshot, gvr schema.GroupVersionResource) operations.ResourceCoverage {
	for _, row := range source.Resources {
		if row.Group == gvr.Group && row.Version == gvr.Version && row.Resource == gvr.Resource {
			return row
		}
	}
	return operations.ResourceCoverage{}
}

type coverageBlockingStore struct {
	graph.GraphStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *coverageBlockingStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.GraphStore.UpsertResource(ctx, r)
}

func TestCRDCoverageInitialDeliveryAndReregistration(t *testing.T) {
	client, meta := coverageClient(t)
	tracker := operations.NewCoverageTracker(time.Minute)
	store := &coverageBlockingStore{GraphStore: memory.New(), entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(store.release) })
	d := New(client, store, WithCoverage(tracker))
	ctx := startCoverageDiscovery(t, d)
	select {
	case <-store.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("initial callback missing")
	}
	row := coverageRow(crdCoverage(t, tracker), coverageFooGVR)
	if row.InitialDeliveryDone || row.State != operations.CoverageInitializing {
		t.Fatal("cache/list success substituted for callback completion")
	}
	releaseOnce.Do(func() { close(store.release) })
	waitFor(t, 3*time.Second, "initial handlers and watches", func() bool { return crdCoverage(t, tracker).State == operations.CoverageObserved })
	before := coverageRow(crdCoverage(t, tracker), coverageFooGVR)
	// A real tombstone must stop the registration, leaving evidence and graph.
	d.onCRDDelete(ctx, cache.DeletedFinalStateUnknown{Obj: meta})
	row = coverageRow(crdCoverage(t, tracker), coverageFooGVR)
	if !slices.Contains(row.Reasons, operations.CoverageCollectorStopped) || len(d.RegisteredGVRs()) != 0 {
		t.Fatal("tombstone failed to stop collector")
	}
	foo, err := client.Resource(coverageFooGVR).Namespace("default").Get(ctx, "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	foo.SetResourceVersion("2")
	if _, err = client.Resource(coverageFooGVR).Namespace("default").Update(ctx, foo, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	d.onCRDAdd(ctx, meta)
	waitFor(t, 3*time.Second, "fresh informer after re-registration", func() bool {
		r, err := store.GetResource(ctx, "default/Foo/demo")
		row := coverageRow(crdCoverage(t, tracker), coverageFooGVR)
		return err == nil && r.ResourceVersion == "2" && row.State == operations.CoverageObserved && row.ContinuityRevision > before.ContinuityRevision
	})
	encoded, err := json.Marshal(crdCoverage(t, tracker))
	if err != nil || strings.Contains(string(encoded), "synthetic-payload-canary") || strings.Contains(string(encoded), "demo") {
		t.Fatal("object payload or name entered coverage")
	}
}

func TestCRDCoverageListAndWatchDenials(t *testing.T) {
	for _, target := range []schema.GroupVersionResource{crdGVR, coverageFooGVR} {
		for _, verb := range []string{"list", "watch"} {
			t.Run(target.Resource+"/"+verb, func(t *testing.T) {
				client, _ := coverageClient(t)
				client.PrependReactor(verb, target.Resource, func(clienttesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(target.GroupResource(), "", errors.New("synthetic-denial-canary"))
				})
				if verb == "watch" {
					client.PrependWatchReactor(target.Resource, func(clienttesting.Action) (bool, watch.Interface, error) {
						return true, nil, apierrors.NewForbidden(target.GroupResource(), "", errors.New("synthetic-denial-canary"))
					})
				}
				tracker := operations.NewCoverageTracker(time.Minute)
				startCoverageDiscovery(t, New(client, memory.New(), WithCoverage(tracker)))
				want := operations.CoverageListDenied
				if verb == "watch" {
					want = operations.CoverageWatchDenied
				}
				waitFor(t, 3*time.Second, "denial evidence", func() bool { return slices.Contains(coverageRow(crdCoverage(t, tracker), target).Reasons, want) })
				encoded, _ := json.Marshal(crdCoverage(t, tracker))
				if strings.Contains(string(encoded), "synthetic-denial-canary") {
					t.Fatal("raw error entered evidence")
				}
			})
		}
	}
}

type coverageFailStore struct {
	graph.GraphStore
	fail atomic.Bool
}

func (s *coverageFailStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	if s.fail.Load() {
		return errors.New("synthetic-write-canary")
	}
	return s.GraphStore.UpsertResource(ctx, r)
}
func (s *coverageFailStore) DeleteResource(ctx context.Context, id string) error {
	if s.fail.Load() {
		return errors.New("synthetic-delete-canary")
	}
	return s.GraphStore.DeleteResource(ctx, id)
}
func (s *coverageFailStore) UpsertEdge(ctx context.Context, e graph.Edge) error {
	if s.fail.Load() {
		return errors.New("synthetic-edge-canary")
	}
	return s.GraphStore.UpsertEdge(ctx, e)
}

func TestCRDCoverageFailedInitialWriteStaysPartial(t *testing.T) {
	client, meta := coverageClient(t)
	tracker := operations.NewCoverageTracker(time.Minute)
	store := &coverageFailStore{GraphStore: memory.New()}
	store.fail.Store(true)
	d := New(client, store, WithCoverage(tracker))
	ctx := startCoverageDiscovery(t, d)
	waitFor(t, 3*time.Second, "failed initial write delivered", func() bool {
		row := coverageRow(crdCoverage(t, tracker), coverageFooGVR)
		return row.InitialDeliveryDone && slices.Contains(row.Reasons, operations.CoveragePersistenceFailed)
	})
	store.fail.Store(false)
	d.onCRDDelete(ctx, meta)
	d.onCRDAdd(ctx, meta)
	waitFor(t, 3*time.Second, "new initial callback", func() bool { return coverageRow(crdCoverage(t, tracker), coverageFooGVR).InitialDeliveryDone })
	row := coverageRow(crdCoverage(t, tracker), coverageFooGVR)
	if row.State != operations.CoveragePartial || !slices.Contains(row.Reasons, operations.CoveragePersistenceFailed) {
		t.Fatal("successful re-list erased prior write gap")
	}
}

type coverageRegoFunc func(context.Context, graph.Resource) ([]graph.Edge, error)

func (f coverageRegoFunc) EvaluateForResource(ctx context.Context, r graph.Resource) ([]graph.Edge, error) {
	return f(ctx, r)
}

func TestCRDCoverageProcessingExtractionAndDeleteGaps(t *testing.T) {
	for _, stage := range []string{"decode", "delete_decode", "extract", "edge", "delete"} {
		t.Run(stage, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			source, err := tracker.BeginSource("", operations.CoverageSourceCRD, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			token, err := source.BeginType(coverageFooGVR, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			token.InitialDeliveryComplete(coverageFooGVR)
			token.WatchStarted(coverageFooGVR)
			store := &coverageFailStore{GraphStore: memory.New()}
			d := New(nil, store)
			_, obj := coverageObjects(t)
			want := operations.CoveragePersistenceFailed
			switch stage {
			case "decode":
				d.handleUpsert(context.Background(), coverageFooGVR, "Foo", (*unstructured.Unstructured)(nil), token)
				want = operations.CoverageProcessingFailed
			case "delete_decode":
				d.handleDelete(context.Background(), coverageFooGVR, cache.DeletedFinalStateUnknown{Obj: "bad"}, "Foo", token)
				want = operations.CoverageProcessingFailed
			case "extract":
				d.rego = coverageRegoFunc(func(context.Context, graph.Resource) ([]graph.Edge, error) {
					return nil, errors.New("synthetic-extraction-canary")
				})
				d.handleUpsert(context.Background(), coverageFooGVR, "Foo", obj, token)
				want = operations.CoverageExtractionFailed
			case "edge":
				d.rego = coverageRegoFunc(func(context.Context, graph.Resource) ([]graph.Edge, error) {
					store.fail.Store(true)
					return []graph.Edge{{From: "default/Foo/demo", To: "default/Foo/other", Type: graph.EdgeTypeOwns}}, nil
				})
				d.handleUpsert(context.Background(), coverageFooGVR, "Foo", obj, token)
			case "delete":
				store.fail.Store(true)
				d.handleDelete(context.Background(), coverageFooGVR, cache.DeletedFinalStateUnknown{Obj: obj}, "Foo", token)
			}
			row := coverageRow(crdCoverage(t, tracker), coverageFooGVR)
			if row.State != operations.CoveragePartial || !slices.Contains(row.Reasons, want) {
				t.Fatalf("gap not recorded: %+v", row)
			}
		})
	}
}

func TestCRDCoverageCapacityDoesNotDisableCollection(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	source, err := tracker.BeginSource("", operations.CoverageSourceCRD, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := range operations.MaxCoverageTypes {
		gvr := coverageFooGVR
		gvr.Resource = fmt.Sprintf("retired%d", i)
		token, err := source.BeginType(gvr, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		token.Stop(time.Now())
	}
	client, _ := coverageClient(t)
	store := memory.New()
	d := New(client, store, WithCoverage(tracker))
	ctx := startCoverageDiscovery(t, d)
	waitFor(t, 3*time.Second, "object collected despite evidence capacity", func() bool { _, err := store.GetResource(ctx, "default/Foo/demo"); return err == nil })
	if got := crdCoverage(t, tracker); !got.InventoryLimited || len(got.Resources) != operations.MaxCoverageTypes || got.State == operations.CoverageObserved {
		t.Fatal("overflow hid lost evidence or exceeded cap")
	}
}

func TestCRDCoverageNeverRegistersCoreOrSecret(t *testing.T) {
	client, _ := coverageClient(t)
	d := New(client, memory.New())
	for _, tc := range []struct {
		gvr  schema.GroupVersionResource
		kind string
	}{
		{schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, "Secret"},
		{coverageFooGVR, "Secret"},
	} {
		identity := registrationIdentity{name: tc.gvr.Resource + "." + tc.gvr.Group, uid: "synthetic-crd-uid", gvr: tc.gvr, kind: tc.kind, scope: "Namespaced"}
		if err := d.reconcileCRD(context.Background(), identity, true); err == nil {
			t.Fatal("accepted Secret collector")
		}
	}
	if len(client.Actions()) != 0 {
		t.Fatal("invalid registration reached Kubernetes client")
	}
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/cache"
)

var reconciliationV2 = schema.GroupVersionResource{Group: "example.com", Version: "v2", Resource: "foos"}

var reconciliationBar = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "bars"}

// No metadata informer is needed here: tests deliver its ordered callbacks
// directly, while exercising the real child list/watch and handler lifetimes.
func reconciliationDiscovery(t *testing.T, store graph.GraphStore, objects ...runtime.Object) (*Discovery, *dynamicfake.FakeDynamicClient, *operations.CoverageTracker, context.Context) {
	t.Helper()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		coverageFooGVR: "FooList", reconciliationV2: "FooList", reconciliationBar: "BarList",
	}, objects...)
	tracker := operations.NewCoverageTracker(time.Minute)
	d := New(client, store, WithCoverage(tracker))
	var err error
	d.coverageSession, err = tracker.BeginSource("", operations.CoverageSourceCRD, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d.metaCoverage = d.beginCoverageType(crdGVR)
	d.metaCoverage.InitialDeliveryComplete(crdGVR)
	d.metaCoverage.WatchStarted(crdGVR)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		d.shutdown()
		d.coverageSession.Stop(time.Now())
		waitFor(t, 3*time.Second, "all callback families retired", func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return len(d.families) == 0
		})
	})
	return d, client, tracker, ctx
}

func reconciliationEntry(d *Discovery, name string) *informerEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.informers[name]
}

func mutateCRD(t *testing.T, meta *unstructured.Unstructured, mutate func(*apiextensionsv1.CustomResourceDefinition)) *unstructured.Unstructured {
	t.Helper()
	crd, ok := toCRD(meta)
	if !ok {
		t.Fatal("invalid fixture CRD")
	}
	mutate(crd)
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(crd)
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: obj}
}

func selectV2(crd *apiextensionsv1.CustomResourceDefinition) {
	crd.Spec.Versions[0].Storage = false
	crd.Spec.Versions = append(crd.Spec.Versions, apiextensionsv1.CustomResourceDefinitionVersion{Name: "v2", Served: true, Storage: true})
}

func TestCRDReconciliationReplacesObservationIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*apiextensionsv1.CustomResourceDefinition)
	}{
		{"storage version", selectV2},
		{"served fallback", func(crd *apiextensionsv1.CustomResourceDefinition) {
			crd.Spec.Versions[0].Served = false
			crd.Spec.Versions = append(crd.Spec.Versions, apiextensionsv1.CustomResourceDefinitionVersion{Name: "v2", Served: true})
		}},
		{"kind", func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Names.Kind = "OtherFoo" }},
		{"scope", func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Scope = apiextensionsv1.ClusterScoped }},
		{"incarnation", func(crd *apiextensionsv1.CustomResourceDefinition) { crd.UID = "replacement-crd-uid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, tracker, ctx := reconciliationDiscovery(t, memory.New())
			meta, _ := coverageObjects(t)
			d.onCRDAdd(ctx, meta)
			waitFor(t, 3*time.Second, "old selection observed", func() bool {
				return coverageRow(crdCoverage(t, tracker), coverageFooGVR).State == operations.CoverageObserved
			})
			old := reconciliationEntry(d, meta.GetName())
			old.coverage.RecordGap(coverageFooGVR, operations.CoveragePersistenceFailed, time.Now())
			before := coverageRow(crdCoverage(t, tracker), coverageFooGVR)
			updated := mutateCRD(t, meta, tc.mutate)
			d.onCRDAdd(ctx, updated)
			fresh := reconciliationEntry(d, meta.GetName())
			if fresh == nil || fresh == old || fresh.coverage == old.coverage || fresh.identity == old.identity {
				t.Fatal("changed identity retained its old registration/token")
			}
			if got := d.RegisteredGVRs(); !reflect.DeepEqual(got, []schema.GroupVersionResource{fresh.identity.gvr}) {
				t.Fatalf("more than one selected endpoint: %v", got)
			}
			waitFor(t, 3*time.Second, "fresh initial delivery and watch", func() bool {
				row := coverageRow(crdCoverage(t, tracker), fresh.identity.gvr)
				return row.InitialDeliveryDone && row.WatchEstablished
			})
			row := coverageRow(crdCoverage(t, tracker), coverageFooGVR)
			if !slices.Contains(row.Reasons, operations.CoveragePersistenceFailed) {
				t.Fatal("replacement erased a known persistence gap")
			}
			if fresh.identity.gvr == coverageFooGVR {
				if row.ContinuityRevision <= before.ContinuityRevision {
					t.Fatal("same-GVR shape change did not advance continuity")
				}
			} else if !slices.Contains(row.Reasons, operations.CoverageCollectorStopped) {
				t.Fatal("retired version still reports active coverage")
			}
			beforeLate := coverageRow(crdCoverage(t, tracker), fresh.identity.gvr)
			old.coverage.RecordGap(coverageFooGVR, operations.CoverageExtractionFailed, time.Now())
			old.coverage.WatchEnded(coverageFooGVR, 0, operations.CoverageWatchFailed, time.Now())
			if after := coverageRow(crdCoverage(t, tracker), fresh.identity.gvr); !reflect.DeepEqual(beforeLate, after) {
				t.Fatal("late old callback changed successor coverage")
			}
		})
	}
}

func TestCRDReconciliationResyncAndSchemaDoNotReset(t *testing.T) {
	d, _, tracker, ctx := reconciliationDiscovery(t, memory.New())
	meta, _ := coverageObjects(t)
	d.onCRDAdd(ctx, meta)
	waitFor(t, 3*time.Second, "selection observed", func() bool {
		return coverageRow(crdCoverage(t, tracker), coverageFooGVR).State == operations.CoverageObserved
	})
	old := reconciliationEntry(d, meta.GetName())
	before := coverageRow(crdCoverage(t, tracker), coverageFooGVR)
	updated := mutateCRD(t, meta, func(crd *apiextensionsv1.CustomResourceDefinition) {
		crd.ResourceVersion = "new-opaque-version"
		crd.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue}}
		crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Description = "schema-only change"
	})
	for range 3 {
		d.onCRDAdd(ctx, updated)
	}
	if reconciliationEntry(d, meta.GetName()) != old || !reflect.DeepEqual(before, coverageRow(crdCoverage(t, tracker), coverageFooGVR)) {
		t.Fatal("schema/status/resync restarted healthy observation")
	}
}

func TestCRDReconciliationUnservedAndUIDBoundDeletion(t *testing.T) {
	store := memory.New()
	meta, foo := coverageObjects(t)
	d, _, tracker, ctx := reconciliationDiscovery(t, store, foo)
	d.onCRDAdd(ctx, meta)
	waitFor(t, 3*time.Second, "initial resource delivered", func() bool {
		return coverageRow(crdCoverage(t, tracker), coverageFooGVR).State == operations.CoverageObserved
	})
	unserved := mutateCRD(t, meta, func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Versions[0].Served = false })
	d.onCRDAdd(ctx, unserved)
	if len(d.RegisteredGVRs()) != 0 || !slices.Contains(coverageRow(crdCoverage(t, tracker), coverageFooGVR).Reasons, operations.CoverageCollectorStopped) {
		t.Fatal("no served version left its old collector active")
	}
	if _, err := store.GetResource(ctx, "default/Foo/demo"); err != nil {
		t.Fatal("retiring registration deleted diagnostic graph data")
	}
	updated := mutateCRD(t, meta, func(crd *apiextensionsv1.CustomResourceDefinition) {
		selectV2(crd)
		crd.UID = "replacement-crd-uid"
	})
	d.onCRDAdd(ctx, updated)
	current := reconciliationEntry(d, meta.GetName())
	// A stale old-incarnation tombstone, including no served version, is harmless.
	d.onCRDDelete(ctx, &cache.DeletedFinalStateUnknown{Obj: unserved})
	if reconciliationEntry(d, meta.GetName()) != current {
		t.Fatal("old incarnation stopped replacement")
	}
	// The current UID resolves the stored v2 selection, not this stale v1 spec.
	sameUIDOldSpec := meta.DeepCopy()
	sameUIDOldSpec.SetUID(updated.GetUID())
	d.onCRDDelete(ctx, cache.DeletedFinalStateUnknown{Obj: sameUIDOldSpec})
	if len(d.RegisteredGVRs()) != 0 {
		t.Fatal("delete recomputed a GVR instead of stopping the owned selection")
	}
	d.onCRDAdd(ctx, updated)
	d.onCRDDelete(ctx, &cache.DeletedFinalStateUnknown{Obj: mutateCRD(t, updated, func(crd *apiextensionsv1.CustomResourceDefinition) {
		for i := range crd.Spec.Versions {
			crd.Spec.Versions[i].Served = false
		}
	})})
	if len(d.RegisteredGVRs()) != 0 {
		t.Fatal("delete with no served version failed to resolve registration")
	}
}

func TestCRDReconciliationInvalidReplacementStopsOldSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*apiextensionsv1.CustomResourceDefinition)
	}{
		{"missing UID", func(crd *apiextensionsv1.CustomResourceDefinition) { crd.UID = "" }},
		{"unknown scope", func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Scope = "Unknown" }},
		{"Secret kind", func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Names.Kind = "Secret" }},
		{"subresource", func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Names.Plural = "foos/status" }},
		{"core group", func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Group = "" }},
		{"malformed version", func(crd *apiextensionsv1.CustomResourceDefinition) { crd.Spec.Versions[0].Name = "v1/evil" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, tracker, ctx := reconciliationDiscovery(t, memory.New())
			meta, _ := coverageObjects(t)
			d.onCRDAdd(ctx, meta)
			d.onCRDAdd(ctx, mutateCRD(t, meta, tc.mutate))
			if len(d.RegisteredGVRs()) != 0 || !slices.Contains(coverageRow(crdCoverage(t, tracker), crdGVR).Reasons, operations.CoverageProcessingFailed) {
				t.Fatal("invalid replacement left obsolete coverage qualified")
			}
		})
	}
}

type reconciliationBlockingStore struct {
	graph.GraphStore
	delete  bool
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *reconciliationBlockingStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	if !s.delete && r.Kind == "Foo" {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	// Deliberately use the real context-ignoring memory store, not a mock that
	// makes cancellation look stronger than the actual persistence contract.
	return s.GraphStore.UpsertResource(ctx, r)
}

func (s *reconciliationBlockingStore) DeleteResource(ctx context.Context, id string) error {
	if s.delete {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	return s.GraphStore.DeleteResource(ctx, id)
}

func TestCRDReconciliationOrdersInflightWritesAndDeletes(t *testing.T) {
	for _, action := range []string{"write/version switch", "write/delete recreate", "delete/delete recreate"} {
		t.Run(action, func(t *testing.T) {
			meta, foo := coverageObjects(t)
			store := &reconciliationBlockingStore{GraphStore: memory.New(), delete: action == "delete/delete recreate", entered: make(chan struct{}), release: make(chan struct{})}
			var released sync.Once
			defer released.Do(func() { close(store.release) })
			d, client, tracker, ctx := reconciliationDiscovery(t, store, foo)
			var regoCalls atomic.Int32
			d.rego = coverageRegoFunc(func(context.Context, graph.Resource) ([]graph.Edge, error) { regoCalls.Add(1); return nil, nil })
			d.onCRDAdd(ctx, meta)
			if store.delete {
				waitFor(t, 3*time.Second, "old initial delivery", func() bool {
					return coverageRow(crdCoverage(t, tracker), coverageFooGVR).State == operations.CoverageObserved
				})
				if err := client.Resource(coverageFooGVR).Namespace("default").Delete(ctx, "demo", metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-store.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("old callback did not enter store")
			}
			gvr := coverageFooGVR
			updated := meta.DeepCopy()
			if action == "write/version switch" {
				updated = mutateCRD(t, meta, selectV2)
				gvr = reconciliationV2
				foo.SetAPIVersion("example.com/v2")
			} else {
				d.onCRDDelete(ctx, meta)
				updated.SetUID("replacement-crd-uid")
			}
			foo.SetResourceVersion("successor-rv")
			if gvr == reconciliationV2 || store.delete {
				if _, err := client.Resource(gvr).Namespace("default").Create(ctx, foo, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := client.Resource(gvr).Namespace("default").Update(ctx, foo, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			d.onCRDAdd(ctx, updated)
			waitFor(t, 3*time.Second, "successor list/watch running", func() bool {
				row := coverageRow(crdCoverage(t, tracker), gvr)
				return row.WatchEstablished && !row.LastListSuccess.IsZero()
			})
			fresh := reconciliationEntry(d, meta.GetName())
			if fresh.synced() || coverageRow(crdCoverage(t, tracker), gvr).InitialDeliveryDone {
				t.Fatal("successor bypassed in-flight old mutation")
			}
			released.Do(func() { close(store.release) })
			waitFor(t, 3*time.Second, "successor data delivered last", func() bool {
				r, err := store.GetResource(ctx, "default/Foo/demo")
				return err == nil && r.ResourceVersion == "successor-rv" && r.GroupVersion == foo.GetAPIVersion() && coverageRow(crdCoverage(t, tracker), gvr).InitialDeliveryDone
			})
			wantCalls := int32(1)
			if store.delete {
				wantCalls++ // initial write before the blocked delete
			}
			if got := regoCalls.Load(); got != wantCalls {
				t.Fatalf("cancelled callback continued to extraction: %d calls, want %d", got, wantCalls)
			}
		})
	}
}

func TestCRDReconciliationCancelsInflightExtractionBeforeEdges(t *testing.T) {
	meta, foo := coverageObjects(t)
	store := memory.New()
	d, _, tracker, ctx := reconciliationDiscovery(t, store, foo)
	entered, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	defer released.Do(func() { close(release) })
	d.rego = coverageRegoFunc(func(_ context.Context, r graph.Resource) ([]graph.Edge, error) {
		if r.GroupVersion == "example.com/v1" {
			close(entered)
			<-release
			return []graph.Edge{{From: r.ID(), To: "default/Foo/old-target", Type: graph.EdgeTypeOwns}}, nil
		}
		return nil, nil
	})
	d.onCRDAdd(ctx, meta)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("old extraction did not start")
	}
	d.onCRDAdd(ctx, mutateCRD(t, meta, selectV2))
	released.Do(func() { close(release) })
	waitFor(t, 3*time.Second, "successor delivery complete", func() bool { return coverageRow(crdCoverage(t, tracker), reconciliationV2).InitialDeliveryDone })
	edges, err := store.ListEdges(ctx, "default/Foo/demo", graph.DirectionOutgoing)
	if err != nil || len(edges) != 0 {
		t.Fatal("retired extraction persisted stale edges")
	}
}

func TestCRDReconciliationBlockedFamilyDoesNotBlockOtherCRDs(t *testing.T) {
	meta, foo := coverageObjects(t)
	bar := foo.DeepCopy()
	bar.SetKind("Bar")
	bar.SetName("independent")
	barMeta := mutateCRD(t, meta, func(crd *apiextensionsv1.CustomResourceDefinition) {
		crd.Name = "bars.example.com"
		crd.UID = "independent-crd-uid"
		crd.Spec.Names.Plural, crd.Spec.Names.Kind = "bars", "Bar"
	})
	store := &reconciliationBlockingStore{GraphStore: memory.New(), entered: make(chan struct{}), release: make(chan struct{})}
	var released sync.Once
	defer released.Do(func() { close(store.release) })
	d, _, tracker, ctx := reconciliationDiscovery(t, store, foo, bar)
	d.onCRDAdd(ctx, meta)
	select {
	case <-store.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first CRD did not block")
	}
	d.onCRDAdd(ctx, barMeta)
	waitFor(t, 3*time.Second, "independent CRD delivered while first remains blocked", func() bool {
		_, err := store.GetResource(ctx, "default/Bar/independent")
		return err == nil && coverageRow(crdCoverage(t, tracker), reconciliationBar).State == operations.CoverageObserved
	})
	if coverageRow(crdCoverage(t, tracker), coverageFooGVR).InitialDeliveryDone {
		t.Fatal("blocked CRD falsely completed initial delivery")
	}
}

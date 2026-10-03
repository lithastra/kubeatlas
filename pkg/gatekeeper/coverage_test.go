// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package gatekeeper

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

	"github.com/lithastra/kubeatlas/pkg/discovery"
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

var coverageConstraintGVR = schema.GroupVersionResource{Group: constraintGroup, Version: constraintVersion, Resource: "k8srequiredlabels"}

func coverageObjects() (*unstructured.Unstructured, *unstructured.Unstructured) {
	template := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "templates.gatekeeper.sh/v1", "kind": "ConstraintTemplate", "metadata": map[string]any{"name": "k8srequiredlabels"},
		"spec": map[string]any{"crd": map[string]any{"spec": map[string]any{"names": map[string]any{"kind": "K8sRequiredLabels"}}}},
	}}
	constraint := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "constraints.gatekeeper.sh/v1beta1", "kind": "K8sRequiredLabels", "metadata": map[string]any{"name": "test-labels", "resourceVersion": "1"},
		"spec": map[string]any{"synthetic": "payload-canary"},
	}}
	return template, constraint
}

func coverageClient() (*dynamicfake.FakeDynamicClient, *unstructured.Unstructured) {
	template, constraint := coverageObjects()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		constraintTemplateGVR: "ConstraintTemplateList", coverageConstraintGVR: "K8sRequiredLabelsList",
	})
	// Explicit GVR: Gatekeeper constraint plurals are lowercase kinds, not the
	// generic pluralization convention used by the fake tracker's Add method.
	if err := client.Tracker().Create(constraintTemplateGVR, template, ""); err != nil {
		panic(err)
	}
	if err := client.Tracker().Create(coverageConstraintGVR, constraint, ""); err != nil {
		panic(err)
	}
	return client, template
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
			t.Error("collector did not stop")
		}
		if d.dynMgr.Started() || len(d.dynMgr.ActiveGVRs()) != 0 {
			t.Error("dynamic manager survived collector shutdown")
		}
	})
	return ctx
}

func gatekeeperCoverage(t *testing.T, tracker *operations.CoverageTracker) operations.CoverageSourceSnapshot {
	t.Helper()
	out, err := tracker.SnapshotCoverage(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range out.Sources {
		if source.Source == operations.CoverageSourceGatekeeper {
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
	once             sync.Once
	entered, release chan struct{}
}

func (s *coverageBlockingStore) UpsertResource(ctx context.Context, r graph.Resource) error {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.GraphStore.UpsertResource(ctx, r)
}

func TestGatekeeperCoverageInitialDeliveryAndReregistration(t *testing.T) {
	client, template := coverageClient()
	tracker := operations.NewCoverageTracker(time.Minute)
	store := &coverageBlockingStore{GraphStore: memory.New(), entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	defer once.Do(func() { close(store.release) })
	manager := discovery.NewDynamicInformerManager(client)
	d := New(client, store, nilRegistry{}, manager, WithCoverage(tracker), WithDiscovery(&fakeGKDiscovery{available: map[string]bool{gkGV(): true}}))
	ctx := startCoverageDiscovery(t, d)
	select {
	case <-store.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("initial handler missing")
	}
	row := coverageRow(gatekeeperCoverage(t, tracker), coverageConstraintGVR)
	if row.InitialDeliveryDone || row.State != operations.CoverageInitializing {
		t.Fatal("cache sync substituted for handler completion")
	}
	once.Do(func() { close(store.release) })
	waitFor(t, 3*time.Second, "initial processing and watches", func() bool { return gatekeeperCoverage(t, tracker).State == operations.CoverageObserved })
	before := coverageRow(gatekeeperCoverage(t, tracker), coverageConstraintGVR)
	d.onTemplate(ctx, template)
	if row := coverageRow(gatekeeperCoverage(t, tracker), coverageConstraintGVR); row.ContinuityRevision != before.ContinuityRevision || !row.InitialDeliveryDone {
		t.Fatal("duplicate registration reset evidence")
	}
	d.onTemplateDelete(cache.DeletedFinalStateUnknown{Obj: template})
	if manager.Has(coverageConstraintGVR) || !slices.Contains(coverageRow(gatekeeperCoverage(t, tracker), coverageConstraintGVR).Reasons, operations.CoverageCollectorStopped) {
		t.Fatal("tombstone did not stop child")
	}
	obj, err := client.Resource(coverageConstraintGVR).Get(ctx, "test-labels", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obj.SetResourceVersion("2")
	if _, err := client.Resource(coverageConstraintGVR).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	d.onTemplate(ctx, template)
	id := discovery.UnstructuredToResource(obj, "K8sRequiredLabels").ID()
	waitFor(t, 3*time.Second, "fresh child after re-registration", func() bool {
		r, err := store.GetResource(ctx, id)
		row := coverageRow(gatekeeperCoverage(t, tracker), coverageConstraintGVR)
		return err == nil && r.ResourceVersion == "2" && row.State == operations.CoverageObserved && row.ContinuityRevision > before.ContinuityRevision
	})
	encoded, _ := json.Marshal(gatekeeperCoverage(t, tracker))
	if strings.Contains(string(encoded), "payload-canary") || strings.Contains(string(encoded), "test-labels") {
		t.Fatal("payload or object name entered coverage")
	}
}

func TestGatekeeperCoverageAbsentThenAdvertised(t *testing.T) {
	client, _ := coverageClient()
	tracker := operations.NewCoverageTracker(time.Minute)
	disco := &fakeGKDiscovery{available: map[string]bool{}}
	d := New(client, memory.New(), nilRegistry{}, discovery.NewDynamicInformerManager(client), WithCoverage(tracker), WithDiscovery(disco))
	d.pollEvery = 10 * time.Millisecond
	startCoverageDiscovery(t, d)
	waitFor(t, 3*time.Second, "absent API evidence", func() bool {
		s := gatekeeperCoverage(t, tracker)
		return s.Discovery != nil && s.Discovery.State == operations.SourceAPINotAdvertised
	})
	if len(client.Actions()) != 0 || len(d.dynMgr.ActiveGVRs()) != 0 {
		t.Fatal("absent API started list/watch")
	}
	disco.set(gkGV(), true)
	waitFor(t, 3*time.Second, "supported API and collection", func() bool { return gatekeeperCoverage(t, tracker).State == operations.CoverageObserved })
}

func TestGatekeeperCoverageListAndWatchDenials(t *testing.T) {
	for _, target := range []schema.GroupVersionResource{constraintTemplateGVR, coverageConstraintGVR} {
		for _, verb := range []string{"list", "watch"} {
			t.Run(target.Resource+"/"+verb, func(t *testing.T) {
				client, _ := coverageClient()
				client.PrependReactor(verb, target.Resource, func(clienttesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(target.GroupResource(), "", errors.New("denial-canary"))
				})
				if verb == "watch" {
					client.PrependWatchReactor(target.Resource, func(clienttesting.Action) (bool, watch.Interface, error) {
						return true, nil, apierrors.NewForbidden(target.GroupResource(), "", errors.New("denial-canary"))
					})
				}
				tracker := operations.NewCoverageTracker(time.Minute)
				startCoverageDiscovery(t, New(client, memory.New(), nilRegistry{}, discovery.NewDynamicInformerManager(client), WithCoverage(tracker), WithDiscovery(&fakeGKDiscovery{available: map[string]bool{gkGV(): true}})))
				want := operations.CoverageListDenied
				if verb == "watch" {
					want = operations.CoverageWatchDenied
				}
				waitFor(t, 3*time.Second, "denial evidence", func() bool { return slices.Contains(coverageRow(gatekeeperCoverage(t, tracker), target).Reasons, want) })
				s := gatekeeperCoverage(t, tracker)
				encoded, _ := json.Marshal(s)
				if s.State == operations.CoverageObserved || s.Discovery.State != operations.SourceAPIAdvertised || strings.Contains(string(encoded), "denial-canary") {
					t.Fatal("advertised API hid denial or exposed raw error")
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
		return errors.New("write-canary")
	}
	return s.GraphStore.UpsertResource(ctx, r)
}
func (s *coverageFailStore) DeleteResource(ctx context.Context, id string) error {
	if s.fail.Load() {
		return errors.New("delete-canary")
	}
	return s.GraphStore.DeleteResource(ctx, id)
}
func (s *coverageFailStore) UpsertEdge(ctx context.Context, edge graph.Edge) error {
	if s.fail.Load() {
		return errors.New("edge-canary")
	}
	return s.GraphStore.UpsertEdge(ctx, edge)
}

type coverageExtractFunc func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error)

func (f coverageExtractFunc) ExtractAll(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	return f(ctx, r, q)
}

func TestGatekeeperCoverageFailedInitialWriteStaysPartial(t *testing.T) {
	client, template := coverageClient()
	tracker := operations.NewCoverageTracker(time.Minute)
	store := &coverageFailStore{GraphStore: memory.New()}
	store.fail.Store(true)
	d := New(client, store, nilRegistry{}, discovery.NewDynamicInformerManager(client), WithCoverage(tracker))
	ctx := startCoverageDiscovery(t, d)
	waitFor(t, 3*time.Second, "failed initial handler", func() bool {
		row := coverageRow(gatekeeperCoverage(t, tracker), coverageConstraintGVR)
		return row.InitialDeliveryDone && slices.Contains(row.Reasons, operations.CoveragePersistenceFailed)
	})
	store.fail.Store(false)
	d.onTemplateDelete(template)
	d.onTemplate(ctx, template)
	waitFor(t, 3*time.Second, "replacement handler", func() bool {
		return coverageRow(gatekeeperCoverage(t, tracker), coverageConstraintGVR).InitialDeliveryDone
	})
	row := coverageRow(gatekeeperCoverage(t, tracker), coverageConstraintGVR)
	if row.State != operations.CoveragePartial || !slices.Contains(row.Reasons, operations.CoveragePersistenceFailed) {
		t.Fatal("successful re-list erased earlier write gap")
	}
}

func TestGatekeeperCoverageProcessingExtractionAndDeleteGaps(t *testing.T) {
	for _, stage := range []string{"decode", "delete_decode", "extract", "edge", "delete"} {
		t.Run(stage, func(t *testing.T) {
			tracker := operations.NewCoverageTracker(time.Minute)
			source, err := tracker.BeginSource("", operations.CoverageSourceGatekeeper, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			token, err := source.BeginType(coverageConstraintGVR, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			token.InitialDeliveryComplete(coverageConstraintGVR)
			token.WatchStarted(coverageConstraintGVR)
			store := &coverageFailStore{GraphStore: memory.New()}
			d := New(nil, store, nilRegistry{}, nil)
			_, obj := coverageObjects()
			want := operations.CoveragePersistenceFailed
			switch stage {
			case "decode":
				d.handleConstraint(context.Background(), "K8sRequiredLabels", (*unstructured.Unstructured)(nil), token)
				want = operations.CoverageProcessingFailed
			case "delete_decode":
				d.handleConstraintDelete(context.Background(), "K8sRequiredLabels", cache.DeletedFinalStateUnknown{Obj: "bad"}, token)
				want = operations.CoverageProcessingFailed
			case "extract":
				d.extractor = coverageExtractFunc(func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error) {
					return nil, errors.New("extract-canary")
				})
				d.handleConstraint(context.Background(), "K8sRequiredLabels", obj, token)
				want = operations.CoverageExtractionFailed
			case "edge":
				d.extractor = coverageExtractFunc(func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error) {
					store.fail.Store(true)
					return []graph.Edge{{From: "a", To: "b", Type: graph.EdgeTypeEnforces}}, nil
				})
				d.handleConstraint(context.Background(), "K8sRequiredLabels", obj, token)
			case "delete":
				store.fail.Store(true)
				d.handleConstraintDelete(context.Background(), "K8sRequiredLabels", cache.DeletedFinalStateUnknown{Obj: obj}, token)
			}
			row := coverageRow(gatekeeperCoverage(t, tracker), coverageConstraintGVR)
			if row.State != operations.CoveragePartial || !slices.Contains(row.Reasons, want) {
				t.Fatalf("missing failure: %+v", row)
			}
		})
	}
}

func TestGatekeeperCoverageCapacityKeepsCollectionAndRejectsSecretKind(t *testing.T) {
	client, template := coverageClient()
	tracker := operations.NewCoverageTracker(time.Minute)
	source, err := tracker.BeginSource("", operations.CoverageSourceGatekeeper, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	meta, err := source.BeginType(constraintTemplateGVR, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < operations.MaxCoverageTypes; i++ {
		gvr := coverageConstraintGVR
		gvr.Resource = fmt.Sprintf("retired%d", i)
		token, err := source.BeginType(gvr, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		token.Stop(time.Now())
	}
	manager := discovery.NewDynamicInformerManager(client)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Start(ctx) }()
	defer func() { cancel(); <-done }()
	waitFor(t, 3*time.Second, "manager startup", manager.Started)
	store := memory.New()
	d := New(client, store, nilRegistry{}, manager)
	d.coverageSession, d.metaCoverage = source, meta
	for _, kind := range []string{"Secret", "bad/type", "bad kind", "bad\x00kind"} {
		invalid := template.DeepCopy()
		if err := unstructured.SetNestedField(invalid.Object, kind, "spec", "crd", "spec", "names", "kind"); err != nil {
			t.Fatal(err)
		}
		d.onTemplate(ctx, invalid)
	}
	if len(client.Actions()) != 0 || len(manager.ActiveGVRs()) != 0 {
		t.Fatal("invalid template caused API observation")
	}
	d.onTemplate(ctx, template)
	_, obj := coverageObjects()
	id := discovery.UnstructuredToResource(obj, "K8sRequiredLabels").ID()
	waitFor(t, 3*time.Second, "collection beyond evidence cap", func() bool { _, err := store.GetResource(ctx, id); return err == nil })
	s := gatekeeperCoverage(t, tracker)
	if !s.InventoryLimited || len(s.Resources) != operations.MaxCoverageTypes || s.State == operations.CoverageObserved {
		t.Fatal("capacity gap hidden")
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("Secret API accessed")
		}
	}
}

func TestGatekeeperCoverageManagerStartupFailureReturns(t *testing.T) {
	client, _ := coverageClient()
	d := New(client, memory.New(), nilRegistry{}, discovery.NewDynamicInformerManager(nil))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Start(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup error lost: %v", err)
	}
}

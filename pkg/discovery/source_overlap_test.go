// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/crd"
	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/gatekeeper"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

type overlapRules func(context.Context, graph.Resource) ([]graph.Edge, error)

func (f overlapRules) EvaluateForResource(ctx context.Context, r graph.Resource) ([]graph.Edge, error) {
	return f(ctx, r)
}

type overlapExtractor func(context.Context, graph.Resource, graph.ResourceLister) ([]graph.Edge, error)

func (f overlapExtractor) ExtractAll(ctx context.Context, r graph.Resource, q graph.ResourceLister) ([]graph.Edge, error) {
	return f(ctx, r, q)
}

type overlapSink struct {
	mu     sync.Mutex
	events []graph.ResourceEvent
}

func (s *overlapSink) Enqueue(e graph.ResourceEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func TestOverlappingDynamicSourcesRetainWriterWithoutHistory(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, first := range []operations.CoverageSource{operations.CoverageSourceCRD, operations.CoverageSourceGatekeeper} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/first=%s", cluster, history, first), func(t *testing.T) {
					testDynamicSourceOverlap(t, cluster, history, first)
				})
			}
		}
	}
}

func testDynamicSourceOverlap(t *testing.T, cluster string, enabled bool, first operations.CoverageSource) {
	t.Helper()
	ctx := context.Background()
	definitions := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	templates := schema.GroupVersionResource{Group: "templates.gatekeeper.sh", Version: "v1", Resource: "constrainttemplates"}
	constraints := schema.GroupVersionResource{Group: "constraints.gatekeeper.sh", Version: "v1beta1", Resource: "k8srequiredlabels"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		definitions: "CustomResourceDefinitionList", templates: "ConstraintTemplateList", constraints: "K8sRequiredLabelsList",
	})
	objects := map[schema.GroupVersionResource]*unstructured.Unstructured{
		definitions: {Object: map[string]any{
			"apiVersion": definitions.GroupVersion().String(), "kind": "CustomResourceDefinition",
			"metadata": map[string]any{"name": "k8srequiredlabels.constraints.gatekeeper.sh", "uid": "crd-owner"},
			"spec": map[string]any{"group": constraints.Group, "scope": "Cluster", "names": map[string]any{"kind": "K8sRequiredLabels", "plural": constraints.Resource},
				"versions": []any{map[string]any{"name": constraints.Version, "served": true, "storage": true}}},
		}},
		templates: {Object: map[string]any{
			"apiVersion": templates.GroupVersion().String(), "kind": "ConstraintTemplate", "metadata": map[string]any{"name": "k8srequiredlabels", "uid": "template-owner"},
			"spec": map[string]any{"crd": map[string]any{"spec": map[string]any{"names": map[string]any{"kind": "K8sRequiredLabels"}}}},
		}},
		constraints: {Object: map[string]any{
			"apiVersion": constraints.GroupVersion().String(), "kind": "K8sRequiredLabels", "metadata": map[string]any{"name": "same", "uid": "object-uid", "resourceVersion": "opaque-first"},
		}},
	}
	for gvr, object := range objects {
		if err := client.Tracker().Create(gvr, object, ""); err != nil {
			t.Fatal(err)
		}
	}
	store := memory.New()
	tracker := operations.NewCoverageTracker(time.Minute)
	repair := discovery.NewMemberRepair(client, store, cluster)
	if cluster == "" {
		repair = discovery.NewStandaloneRepair(client, store)
	}
	sink := &overlapSink{}
	var history *discovery.ResourceHistory
	if enabled {
		history = discovery.NewResourceHistory(store, cluster, sink)
		if cluster == "" {
			history = discovery.NewStandaloneResourceHistory(store, sink)
		}
	}
	row := discovery.UnstructuredToResource(objects[constraints], "K8sRequiredLabels")
	row.ClusterID = cluster
	foreign := row
	foreign.ClusterID = "west"
	if err := store.UpsertResource(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	targets := map[operations.CoverageSource]graph.Resource{}
	for _, source := range []operations.CoverageSource{operations.CoverageSourceCRD, operations.CoverageSourceGatekeeper} {
		target := graph.Resource{ClusterID: cluster, Kind: "Pod", Namespace: "default", Name: string(source)}
		targets[source] = target
		if err := store.UpsertResource(ctx, target); err != nil {
			t.Fatal(err)
		}
	}
	edgesFor := func(source operations.CoverageSource, r graph.Resource) []graph.Edge {
		return []graph.Edge{{From: r.ID(), To: targets[source].ID(), Type: graph.EdgeTypeEnforces}}
	}
	start := func(source operations.CoverageSource) func() {
		child, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		if source == operations.CoverageSourceCRD {
			d := crd.New(client, store, crd.WithClusterID(cluster), crd.WithCoverage(tracker), crd.WithMemberRepair(repair), crd.WithResourceHistory(history),
				crd.WithRegoEvaluator(overlapRules(func(_ context.Context, r graph.Resource) ([]graph.Edge, error) { return edgesFor(source, r), nil })))
			go func() { done <- d.Start(child) }()
		} else {
			d := gatekeeper.New(client, store, overlapExtractor(func(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
				return edgesFor(source, r), nil
			}),
				discovery.NewDynamicInformerManager(client), gatekeeper.WithClusterID(cluster), gatekeeper.WithCoverage(tracker), gatekeeper.WithMemberRepair(repair), gatekeeper.WithResourceHistory(history))
			go func() { done <- d.Start(child) }()
		}
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Errorf("collector exit: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Error("collector did not join")
				}
			})
		}
		t.Cleanup(stop)
		waitUntil(t, 3*time.Second, "source initial delivery and watch", func() bool {
			capture, _ := tracker.SnapshotCoverage(ctx, cluster, nil)
			for _, s := range capture.Sources {
				if s.Source != source {
					continue
				}
				for _, r := range s.Resources {
					if r.Group == constraints.Group && r.Resource == constraints.Resource && r.InitialDeliveryDone && r.WatchEstablished {
						return true
					}
				}
			}
			return false
		})
		return stop
	}
	assertOrigin := func(want graph.ResourceOrigin, qualification string) {
		t.Helper()
		got, err := store.GetResource(ctx, row.ID())
		capture, captureErr := tracker.SnapshotCoverage(ctx, cluster, nil)
		if err != nil || captureErr != nil || got.Origin != want || graph.OriginQualification(got, capture.Origins) != qualification {
			t.Fatalf("stored writer/qualification changed: %+v want %+v (%s): %v %v", got.Origin, want, qualification, err, captureErr)
		}
	}
	second := operations.CoverageSourceCRD
	if first == second {
		second = operations.CoverageSourceGatekeeper
	}
	stopFirst := start(first)
	initial, err := store.GetResource(ctx, row.ID())
	if err != nil || !initial.Origin.Matches(initial) || initial.Origin.Source != string(first) {
		t.Fatal("first source did not supply its own receipt", initial.Origin, err)
	}
	stopSecond := start(second)
	assertOrigin(initial.Origin, "registered")
	stopFirst()
	assertOrigin(initial.Origin, "retired")
	stopSecond()
	if err := store.DeleteEdge(ctx, row.ID(), targets[second].ID(), graph.EdgeTypeEnforces); err != nil {
		t.Fatal(err)
	}
	// Fresh unchanged delivery from the other source must still re-extract edges
	// without adopting or clearing the retired writer receipt.
	stopSecond = start(second)
	assertOrigin(initial.Origin, "retired")
	edges, err := store.ListEdges(ctx, row.ID(), graph.DirectionOutgoing)
	if err != nil || len(edges) != 2 {
		t.Fatal("replay skipped edge recovery", edges, err)
	}
	stopFirst = start(first)
	refreshed, _ := store.GetResource(ctx, row.ID())
	if refreshed.Origin.Source != string(first) || refreshed.Origin.Registration == initial.Origin.Registration {
		t.Fatal("same-source fresh delivery failed to refresh receipt", refreshed.Origin)
	}
	assertOrigin(refreshed.Origin, "registered")
	stopFirst()
	changed := objects[constraints].DeepCopy()
	changed.SetResourceVersion("opaque-next")
	if _, err := client.Resource(constraints).Update(ctx, changed, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 3*time.Second, "actual change adopts the delivering source", func() bool {
		got, _ := store.GetResource(ctx, row.ID())
		return got.ResourceVersion == "opaque-next" && got.Origin.Source == string(second) && got.Origin.Matches(got)
	})
	stopSecond()
	gotForeign, err := store.GetResource(ctx, foreign.ID())
	if err != nil || !graph.SameResourceVersion(gotForeign, foreign) {
		t.Fatal("source overlap changed another member", gotForeign, err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	wantEvents := 0
	if enabled {
		wantEvents = 2
	}
	if len(sink.events) != wantEvents {
		t.Fatalf("source replay changed object history: %+v", sink.events)
	}
	for i, event := range sink.events {
		want := graph.EventTypeAdd
		if i == 1 {
			want = graph.EventTypeUpdate
		}
		if event.ClusterID != cluster || event.Data != nil || event.EventType != want {
			t.Fatal("wrong scoped metadata event", event)
		}
	}
}

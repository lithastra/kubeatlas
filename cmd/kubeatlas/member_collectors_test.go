// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kdiscovery "k8s.io/client-go/discovery"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

var memberPods = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
var memberCRDs = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
var memberFoos = schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "foos"}
var memberTemplates = schema.GroupVersionResource{Group: "templates.gatekeeper.sh", Version: "v1", Resource: "constrainttemplates"}
var memberConstraints = schema.GroupVersionResource{Group: "constraints.gatekeeper.sh", Version: "v1beta1", Resource: "k8srequiredlabels"}

func memberDynamicClient(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		memberPods: "PodList", memberCRDs: "CustomResourceDefinitionList", memberFoos: "FooList", memberTemplates: "ConstraintTemplateList", memberConstraints: "K8sRequiredLabelsList",
	})
	objects := map[schema.GroupVersionResource]*unstructured.Unstructured{
		memberPods:        {Object: map[string]any{"kind": "Pod", "apiVersion": "v1", "metadata": map[string]any{"name": "same", "namespace": "default", "uid": "same-pod-uid", "resourceVersion": "1"}}},
		memberCRDs:        {Object: map[string]any{"kind": "CustomResourceDefinition", "apiVersion": "apiextensions.k8s.io/v1", "metadata": map[string]any{"name": "foos.example.com", "uid": "same-crd-uid"}, "spec": map[string]any{"group": "example.com", "names": map[string]any{"kind": "Foo", "plural": "foos"}, "scope": "Namespaced", "versions": []any{map[string]any{"name": "v1", "served": true, "storage": true}}}}},
		memberFoos:        {Object: map[string]any{"kind": "Foo", "apiVersion": "example.com/v1", "metadata": map[string]any{"name": "same", "namespace": "default", "uid": "same-foo-uid", "resourceVersion": "1"}}},
		memberTemplates:   {Object: map[string]any{"kind": "ConstraintTemplate", "apiVersion": "templates.gatekeeper.sh/v1", "metadata": map[string]any{"name": "k8srequiredlabels"}, "spec": map[string]any{"crd": map[string]any{"spec": map[string]any{"names": map[string]any{"kind": "K8sRequiredLabels"}}}}}},
		memberConstraints: {Object: map[string]any{"kind": "K8sRequiredLabels", "apiVersion": "constraints.gatekeeper.sh/v1beta1", "metadata": map[string]any{"name": "same", "uid": "same-constraint-uid", "resourceVersion": "1"}}},
	}
	for gvr, obj := range objects {
		if err := client.Tracker().Create(gvr, obj, obj.GetNamespace()); err != nil {
			t.Fatal(err)
		}
	}
	return client
}

type memberMetadata struct {
	kdiscovery.DiscoveryInterface
	denied bool
}

func (m memberMetadata) ServerGroupsWithContext(context.Context) (*metav1.APIGroupList, error) {
	return &metav1.APIGroupList{}, nil
}
func (m memberMetadata) ServerResourcesForGroupVersionWithContext(_ context.Context, gv string) (*metav1.APIResourceList, error) {
	if gv == memberTemplates.GroupVersion().String() && m.denied {
		return nil, apierrors.NewForbidden(memberTemplates.GroupResource(), "", errors.New("synthetic-private-member-denial"))
	}
	list := &metav1.APIResourceList{GroupVersion: gv}
	if gv == "v1" {
		list.APIResources = []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"list", "watch"}}}
	}
	if gv == memberTemplates.GroupVersion().String() {
		list.APIResources = []metav1.APIResource{{Name: "constrainttemplates", Kind: "ConstraintTemplate", Verbs: metav1.Verbs{"list", "watch"}}}
	}
	return list, nil
}
func awaitMember(t *testing.T, label string, predicate func() bool) {
	t.Helper()
	until := time.Now().Add(4 * time.Second)
	for time.Now().Before(until) {
		if predicate() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}
func memberSources(t *testing.T, tracker *operations.CoverageTracker, name string) map[operations.CoverageSource]operations.CoverageSourceSnapshot {
	t.Helper()
	snapshot, err := tracker.SnapshotCoverage(context.Background(), name, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[operations.CoverageSource]operations.CoverageSourceSnapshot{}
	for _, source := range snapshot.Sources {
		out[source.Source] = source
	}
	return out
}
func TestMemberFactoryCollectsIndependentCRDsAndGatekeeper(t *testing.T) {
	store := memory.New()
	tracker := operations.NewCoverageTracker(time.Minute)
	metrics := discovery.NewDynamicMetrics()
	var synced atomic.Int32
	factory := memberInformerFactory([]discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{memberPods}), discovery.WithOnSynced(func() { synced.Add(1) })}, tracker, nil, extractor.Default(), metrics)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	east, west := memberDynamicClient(t), memberDynamicClient(t)
	done := make(chan error, 2)
	for name, client := range map[string]*dynamicfake.FakeDynamicClient{"east": east, "west": west} {
		member := factory(name, client, memberMetadata{}, store)
		go func() { done <- member.Start(ctx) }()
	}
	stopped := false
	finish := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		for range 2 {
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("member exit: %v", err)
				}
			case <-time.After(4 * time.Second):
				t.Error("member bundle did not stop")
			}
		}
	}
	t.Cleanup(finish)
	awaitMember(t, "both member collectors", func() bool {
		if synced.Load() != 2 {
			return false
		}
		for _, name := range []string{"east", "west"} {
			sources := memberSources(t, tracker, name)
			if len(sources) != 2 || sources[operations.CoverageSourceCRD].State != operations.CoverageObserved || sources[operations.CoverageSourceGatekeeper].State != operations.CoverageObserved {
				return false
			}
			for _, id := range []string{"default/Foo/same", "/K8sRequiredLabels/same"} {
				if _, err := store.GetResource(ctx, name+":"+id); err != nil {
					return false
				}
			}
		}
		return true
	})
	if metrics.Snapshot().Active != 2 {
		t.Fatal("member dynamic metrics did not aggregate")
	}
	if len(memberSources(t, tracker, "")) != 0 || len(memberSources(t, tracker, "hidden")) != 0 {
		t.Fatal("member source scope leaked")
	}
	before := memberSources(t, tracker, "west")
	if err := east.Resource(memberFoos).Namespace("default").Delete(ctx, "same", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := east.Resource(memberConstraints).Delete(ctx, "same", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitMember(t, "east deletes", func() bool {
		_, a := store.GetResource(ctx, "east:default/Foo/same")
		_, b := store.GetResource(ctx, "east:/K8sRequiredLabels/same")
		return a != nil && b != nil
	})
	for _, id := range []string{"west:default/Foo/same", "west:/K8sRequiredLabels/same"} {
		if _, err := store.GetResource(ctx, id); err != nil {
			t.Fatal("delete crossed member scope")
		}
	}
	after := memberSources(t, tracker, "west")
	for source, evidence := range before {
		if after[source].Generation != evidence.Generation {
			t.Fatal("sibling evidence replaced")
		}
	}
	for _, client := range []*dynamicfake.FakeDynamicClient{east, west} {
		for _, action := range client.Actions() {
			if action.GetResource().Resource == "secrets" {
				t.Fatal("member requested Secret objects")
			}
		}
	}
	finish()
	if metrics.Snapshot().Active != 0 {
		t.Fatal("shutdown left active member registrations")
	}
	for _, name := range []string{"east", "west"} {
		for _, source := range memberSources(t, tracker, name) {
			for _, row := range source.Resources {
				if !slices.Contains(row.Reasons, operations.CoverageCollectorStopped) {
					t.Fatal("returned member retained live source evidence")
				}
			}
		}
	}
	previous := memberSources(t, tracker, "east")
	freshCtx, freshCancel := context.WithCancel(context.Background())
	freshDone := make(chan error, 1)
	go func() { freshDone <- factory("east", east, memberMetadata{}, store).Start(freshCtx) }()
	freshStopped := false
	freshFinish := func() {
		if freshStopped {
			return
		}
		freshStopped = true
		freshCancel()
		select {
		case err := <-freshDone:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("reattached exit: %v", err)
			}
		case <-time.After(4 * time.Second):
			t.Error("reattached member did not stop")
		}
	}
	t.Cleanup(freshFinish)
	awaitMember(t, "fresh member source generations", func() bool {
		sources := memberSources(t, tracker, "east")
		if synced.Load() != 3 || len(sources) != 2 {
			return false
		}
		for kind, old := range previous {
			if sources[kind].Generation <= old.Generation || sources[kind].State != operations.CoverageObserved {
				return false
			}
		}
		return true
	})
	if metrics.Snapshot().Active != 1 {
		t.Fatal("reattached member reused sibling registration count")
	}
	freshFinish()
	if metrics.Snapshot().Active != 0 {
		t.Fatal("reattached member metrics did not close")
	}

}
func TestMemberFactoryKeepsPermissionGapsScoped(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	client := memberDynamicClient(t)
	client.PrependReactor("list", "customresourcedefinitions", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(memberCRDs.GroupResource(), "", errors.New("synthetic-private-member-denial"))
	})
	factory := memberInformerFactory([]discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{memberPods})}, tracker, nil, extractor.Default(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- factory("west", client, memberMetadata{denied: true}, memory.New()).Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("denied member failed to stop")
		}
	})
	awaitMember(t, "qualified member gaps", func() bool {
		sources := memberSources(t, tracker, "west")
		gk := sources[operations.CoverageSourceGatekeeper]
		if gk.Discovery == nil || gk.Discovery.State != operations.SourceAPIDenied {
			return false
		}
		for _, row := range sources[operations.CoverageSourceCRD].Resources {
			if slices.Contains(row.Reasons, operations.CoverageListDenied) {
				return true
			}
		}
		return false
	})
	select {
	case err := <-done:
		t.Fatalf("optional API denial stopped the member: %v", err)
	default:
	}
	raw, _ := json.Marshal(memberSources(t, tracker, "west"))
	if strings.Contains(string(raw), "synthetic-private-member-denial") {
		t.Fatal("raw provider error in evidence")
	}
	if len(memberSources(t, tracker, "east")) != 0 || len(memberSources(t, tracker, "")) != 0 {
		t.Fatal("denial attributed to another cluster")
	}
}

type memberComponentFunc func(context.Context) error

func (f memberComponentFunc) Start(ctx context.Context) error { return f(ctx) }
func TestMemberBundleCancelsAndJoinsSiblingsOnFailure(t *testing.T) {
	want := errors.New("synthetic member failure")
	started := make(chan struct{}, 3)
	failure := make(chan struct{})
	release := make(chan struct{})
	canceled := make(chan struct{})
	group := &memberCollectors{components: []componentStarter{
		memberComponentFunc(func(ctx context.Context) error { started <- struct{}{}; <-failure; return want }),
		memberComponentFunc(func(ctx context.Context) error {
			started <- struct{}{}
			<-ctx.Done()
			close(canceled)
			<-release
			return ctx.Err()
		}),
		memberComponentFunc(func(ctx context.Context) error { started <- struct{}{}; <-ctx.Done(); return ctx.Err() }),
	}}
	done := make(chan error, 1)
	go func() { done <- group.Start(context.Background()) }()
	for range 3 {
		<-started
	}
	close(failure)
	<-canceled
	select {
	case <-done:
		t.Fatal("member returned before sibling finished")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatal("first member error lost")
		}
	case <-time.After(time.Second):
		t.Fatal("member did not join siblings")
	}
}

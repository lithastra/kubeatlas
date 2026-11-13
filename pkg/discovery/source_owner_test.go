// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/cache"
)

func TestOwnedEndpointReconciliationPreservesDataAndRejectsUnknownOwners(t *testing.T) {
	row := storetest.ResourceOriginFixture("")
	owner := row.Origin
	owner.Source = "gatekeeper"
	first := owner.GVR()
	second := first
	second.Resource = "bars"
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{first: "FooList", second: "BarList"})
	manager := NewDynamicInformerManager(client)
	tracker := operations.NewCoverageTracker(time.Minute)
	source, _ := tracker.BeginSource("", operations.CoverageSourceGatekeeper, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("manager failed to join")
		}
	})
	awaitCoverage(t, manager.Started)
	var token *operations.CoverageSession
	build := func(_ context.Context, next *operations.CoverageSession) cache.ResourceEventHandler {
		token = next
		return cache.ResourceEventHandlerFuncs{}
	}
	if err := manager.AddObservedOwned(first, source, owner, build); err != nil {
		t.Fatal(err)
	}
	stored := token.StampOrigin(row)
	if !stored.Origin.Matches(stored) {
		t.Fatal("owner not bound")
	}
	unknown := owner
	unknown.OwnerUID = ""
	unknown.Resource, unknown.Kind = "bars", "Bar"
	if err := manager.AddObservedOwned(second, source, unknown, build); err == nil {
		t.Fatal("unknown owner changed owned registration")
	}
	if !manager.Has(first) || manager.Has(second) {
		t.Fatal("unverified replacement altered ingress")
	}
	changed := owner
	changed.Resource, changed.Kind = "bars", "Bar"
	if err := manager.AddObservedOwned(second, source, changed, build); err != nil {
		t.Fatal(err)
	}
	if manager.Has(first) || !manager.Has(second) {
		t.Fatal("same-owner kind change left old endpoint running")
	}
	capture, _ := tracker.SnapshotCoverage(ctx, "", nil)
	if graph.OriginQualification(stored, capture.Origins) != "retired" {
		t.Fatal("retired endpoint ownership remained active")
	}
	replacement := changed
	replacement.OwnerUID = "replacement-owner"
	if err := manager.AddObservedOwned(second, source, replacement, build); err != nil {
		t.Fatal(err)
	}
	manager.RemoveOwner(changed)
	manager.RemoveUnowned(second)
	if !manager.Has(second) {
		t.Fatal("old/unidentified tombstone stopped replacement")
	}
	// A matching UID resolves the stored endpoint despite an obsolete spec.
	replacement.Resource = "obsolete-spec"
	manager.RemoveOwner(replacement)
	if manager.Has(second) {
		t.Fatal("matching owner did not retire stored endpoint")
	}
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/cache"
)

func TestDynamicCoverageRegistrationOwnershipAndShutdown(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "constraints.gatekeeper.sh", Version: "v1beta1", Resource: "testconstraints"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "TestConstraintList"})
	manager := NewDynamicInformerManager(client)
	tracker := operations.NewCoverageTracker(time.Minute)
	source, err := tracker.BeginSource("", operations.CoverageSourceGatekeeper, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Start(ctx) }()
	defer cancel()
	awaitCoverage(t, manager.Started)
	var child context.Context
	var token *operations.CoverageSession
	build := func(c context.Context, s *operations.CoverageSession) cache.ResourceEventHandler {
		child, token = c, s
		return cache.ResourceEventHandlerFuncs{}
	}
	if err := manager.AddObserved(gvr, source, build); err != nil {
		t.Fatal(err)
	}
	read := func() operations.CoverageSourceSnapshot {
		s, err := tracker.SnapshotCoverage(context.Background(), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		return s.Sources[0]
	}
	awaitCoverage(t, func() bool { return read().Resources[0].State == operations.CoverageObserved })
	before := read()
	if err := manager.AddObserved(gvr, source, func(context.Context, *operations.CoverageSession) cache.ResourceEventHandler {
		t.Error("duplicate invoked builder")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("duplicate reset evidence")
	}
	oldChild, oldToken := child, token
	manager.Remove(gvr)
	if oldChild.Err() == nil {
		t.Fatal("handler did not share registration cancellation")
	}
	if err := manager.AddObserved(gvr, source, build); err != nil {
		t.Fatal(err)
	}
	awaitCoverage(t, func() bool { return read().Resources[0].State == operations.CoverageObserved })
	before = read()
	oldToken.Stop(time.Now())
	oldToken.RecordGap(gvr, operations.CoveragePersistenceFailed, time.Now())
	oldToken.InitialDeliveryComplete(gvr)
	oldToken.WatchStarted(gvr)
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("late callbacks changed replacement evidence")
	}
	if err := manager.Add(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, cache.ResourceEventHandlerFuncs{}); err == nil {
		t.Fatal("Secret informer accepted")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not stop")
	}
	if child.Err() == nil || manager.Started() || manager.Has(gvr) {
		t.Fatal("live registration after shutdown")
	}
	if err := manager.Add(gvr, cache.ResourceEventHandlerFuncs{}); !errors.Is(err, ErrManagerNotStarted) {
		t.Fatal("shutdown accepted registration")
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("Secret API accessed")
		}
	}
}

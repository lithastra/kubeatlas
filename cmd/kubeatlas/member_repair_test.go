// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestMemberFactoryRepairsRetainedRowsWithMemberIsolation(t *testing.T) {
	s := memory.New()
	tracker := operations.NewCoverageTracker(time.Minute)
	client := memberDynamicClient(t)
	factory := memberInformerFactory([]discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{memberPods})}, tracker, nil, extractor.Default(), discovery.NewDynamicMetrics())
	// Stop and reattach the actual production bundle. These rows represent
	// deletes missed while detached, not synthetic delete callbacks.
	start := func() func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- factory("east", client, memberMetadata{}, s).Start(ctx) }()
		stopped := false
		return func() {
			if stopped {
				return
			}
			stopped = true
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("bundle exit: %v", err)
				}
			case <-time.After(4 * time.Second):
				t.Error("member workers did not join")
			}
		}
	}
	stop := start()
	t.Cleanup(stop)
	awaitMember(t, "initial member resources", func() bool { _, err := s.GetResource(context.Background(), "east:default/Foo/same"); return err == nil })
	stop()
	retained := []graph.Resource{
		{ClusterID: "east", Kind: "Pod", Namespace: "default", Name: "missed", GroupVersion: "v1", UID: "old-pod", ResourceVersion: "1"},
		{ClusterID: "east", Kind: "Foo", Namespace: "default", Name: "missed", GroupVersion: "example.com/v1", UID: "old-foo", ResourceVersion: "1"},
		{ClusterID: "east", Kind: "K8sRequiredLabels", Name: "missed", GroupVersion: "constraints.gatekeeper.sh/v1beta1", UID: "old-constraint", ResourceVersion: "1"},
	}
	for _, row := range retained {
		for _, cluster := range []string{"east", "west", ""} {
			row.ClusterID = cluster
			if err := s.UpsertResource(context.Background(), row); err != nil {
				t.Fatal(err)
			}
		}
		row.ClusterID = "east"
		if err := s.UpsertEdge(context.Background(), graph.Edge{From: row.ID(), To: "east:default/Pod/same", Type: graph.EdgeTypeOwns}); err != nil {
			t.Fatal(err)
		}
	}
	oldVersion := retained[1]
	oldVersion.Name = "old-version"
	oldVersion.GroupVersion = "example.com/v0"
	retired := retained[1]
	retired.Kind = "RetiredKind"
	for _, row := range []graph.Resource{oldVersion, retired} {
		_ = s.UpsertResource(context.Background(), row)
	}
	freshStop := start()
	t.Cleanup(freshStop)
	awaitMember(t, "all three collectors repair detached leftovers", func() bool {
		for _, row := range retained {
			if _, err := s.GetResource(context.Background(), row.ID()); err == nil {
				return false
			}
		}
		return true
	})
	for _, row := range retained {
		for _, cluster := range []string{"west", ""} {
			row.ClusterID = cluster
			if _, err := s.GetResource(context.Background(), row.ID()); err != nil {
				t.Fatal("repair crossed member boundary")
			}
		}
	}
	for _, row := range []graph.Resource{oldVersion, retired} {
		if _, err := s.GetResource(context.Background(), row.ID()); err != nil {
			t.Fatal("repair inferred absence from an unobserved endpoint")
		}
	}
	edges, err := s.ListEdges(context.Background(), "east:default/Pod/same", graph.DirectionIncoming)
	if err != nil || len(edges) != 0 {
		t.Fatalf("missed-delete incident edges retained: %+v %v", edges, err)
	}
	for _, action := range client.Actions() {
		if action.GetResource().Resource == "secrets" || action.GetVerb() != "list" && action.GetVerb() != "watch" {
			t.Fatalf("unexpected member request: %s %s", action.GetVerb(), action.GetResource())
		}
	}
}

type blockedMemberRepairStore struct {
	*memory.Store
	entered, release chan struct{}
	once             sync.Once
}

func (s *blockedMemberRepairStore) DeleteResourceVersion(ctx context.Context, row graph.Resource) (bool, error) {
	if row.Name == "missed" {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	return s.Store.DeleteResourceVersion(ctx, row)
}

func TestMemberBundleJoinsBlockedRepairBeforeReleasingName(t *testing.T) {
	s := &blockedMemberRepairStore{Store: memory.New(), entered: make(chan struct{}), release: make(chan struct{})}
	row := graph.Resource{ClusterID: "east", Kind: "Pod", Namespace: "default", Name: "missed", GroupVersion: "v1", UID: "old-pod", ResourceVersion: "1"}
	_ = s.UpsertResource(context.Background(), row)
	tracker := operations.NewCoverageTracker(time.Minute)
	factory := memberInformerFactory([]discovery.InformerOption{discovery.WithCoverage(tracker), discovery.WithGVRs([]schema.GroupVersionResource{memberPods})}, tracker, nil, extractor.Default(), discovery.NewDynamicMetrics())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var released sync.Once
	unblock := func() { released.Do(func() { close(s.release) }) }
	t.Cleanup(func() { cancel(); unblock() })
	go func() { done <- factory("east", memberDynamicClient(t), memberMetadata{}, s).Start(ctx) }()
	select {
	case <-s.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("repair did not reach conditional deletion")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("bundle returned while repair still owned a mutation")
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown error: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("bundle did not join released repair")
	}
	if _, err := s.GetResource(context.Background(), row.ID()); err != nil {
		t.Fatal("cancelled repair removed retained row")
	}
}

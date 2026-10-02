// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
)

func TestStore_ImpactSnapshot(t *testing.T) {
	storetest.RunImpactSnapshot(t, func(_ *testing.T) graph.GraphStore { return New() })
}

func TestStore_ImpactSnapshotCancelledLockWait(t *testing.T) {
	s := New()
	s.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := s.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{})
		finished <- err
	}()
	select {
	case err := <-finished:
		s.mu.Unlock()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lock wait error=%v", err)
		}
	case <-time.After(time.Second):
		s.mu.Unlock()
		t.Fatal("cancelled read remained blocked on writer lock")
	}
	if _, err := s.SnapshotImpact(context.Background(), graph.ImpactSnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestStore_ImpactSnapshotConcurrentAtomicDelete(t *testing.T) {
	s := New()
	ctx := context.Background()
	root := graph.Resource{Kind: "ConfigMap", Name: "settings", Namespace: "demo"}
	child, expected := storetest.ImpactReferenceFixture(t, "Pod", "v1")
	child.ClusterID = ""
	if err := s.UpsertResource(ctx, root); err != nil {
		t.Fatal(err)
	}
	edge := graph.Edge{From: child.ID(), To: root.ID(), Type: graph.EdgeTypeUsesConfigMap}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := s.UpsertResource(ctx, child); err != nil {
				t.Error(err)
				return
			}
			if err := s.UpsertEdge(ctx, edge); err != nil {
				t.Error(err)
				return
			}
			if err := s.DeleteResource(ctx, child.ID()); err != nil {
				t.Error(err)
				return
			}
		}
	})
	defer func() { close(stop); wg.Wait() }()
	for range 300 {
		g, err := s.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// The writer may be between independent upserts, but DeleteResource's
		// atomic cascade must never yield an edge without its child resource.
		if len(g.Edges) > 0 && len(g.Resources) != 2 {
			t.Fatalf("mixed resource/edge snapshot: %+v", g)
		}
		for _, r := range g.Resources {
			if r.ID() == child.ID() && !reflect.DeepEqual(r.Raw, expected.Raw) {
				t.Fatal("reference evidence changed during an atomic read")
			}
		}
	}
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
)

func TestStoreSnapshotMarker(t *testing.T) {
	storetest.RunSnapshotMarker(t, func(*testing.T) graph.GraphStore { return New() })
}
func TestSnapshotMarkerLockCancellationDoesNotWriteLater(t *testing.T) {
	s := New()
	s.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got, err := s.RecordSnapshotMarker(ctx, "prod", graph.SnapshotTriggerManual)
	s.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || got.ID != 0 {
		t.Fatal("lock wait ignored cancellation", got, err)
	}
	markers, err := s.ListSnapshotMeta(context.Background())
	if err != nil || len(markers) != 0 {
		t.Fatal("cancelled lock waiter wrote a marker")
	}
}
func TestSnapshotMarkerCountsShareOneCriticalSection(t *testing.T) {
	s := New()
	a := graph.Resource{ClusterID: "prod", Kind: "Pod", Name: "a"}
	b := graph.Resource{ClusterID: "prod", Kind: "Pod", Name: "b"}
	edge := graph.Edge{From: a.ID(), To: b.ID(), Type: graph.EdgeTypeOwns}
	done := make(chan struct{})
	ready := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		first := true
		for {
			select {
			case <-done:
				return
			default:
			}
			s.mu.Lock()
			s.resources = map[string]graph.Resource{a.ID(): a, b.ID(): b}
			s.outgoing = map[string]map[edgeKey]graph.Edge{a.ID(): {{other: b.ID(), typ: edge.Type}: edge}}
			s.mu.Unlock()
			if first {
				close(ready)
				first = false
			}
			s.mu.Lock()
			s.resources = map[string]graph.Resource{a.ID(): a}
			s.outgoing = map[string]map[edgeKey]graph.Edge{}
			s.mu.Unlock()
		}
	}()
	defer func() { close(done); wg.Wait() }()
	<-ready
	for range 100 {
		m, err := s.RecordSnapshotMarker(context.Background(), "prod", graph.SnapshotTriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		consistent := m.ResourceCount == 1 && m.EdgeCount == 0 || m.ResourceCount == 2 && m.EdgeCount == 1
		if !consistent {
			t.Fatalf("torn graph observation: %+v", m)
		}
	}
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
)

func TestStore_HistoryBounds(t *testing.T) {
	storetest.RunHistoryBounds(t, func(*testing.T) graph.GraphStore { return New() })
}

func TestStore_HistoryBoundsRingEvictionAndLockCancellation(t *testing.T) {
	s := New()
	ctx := context.Background()
	base := time.Now().Truncate(time.Second)
	for i := range maxMemoryEvents + 1 {
		if err := s.AppendEvent(ctx, graph.ResourceEvent{Timestamp: base.Add(time.Duration(i) * time.Second), Kind: "Pod"}); err != nil {
			t.Fatal(err)
		}
	}
	q := graph.HistoryBoundsQuery{From: base, To: base.Add(time.Hour)}
	got, err := s.ReadHistoryBounds(ctx, q)
	if err != nil || !got.FirstEventAt.Equal(base.Add(time.Second)) || !got.LastEventAt.Equal(base.Add(maxMemoryEvents*time.Second)) {
		t.Fatal("ring eviction was hidden")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	got, err = s.ReadHistoryBounds(bounded, q)
	if !errors.Is(err, context.DeadlineExceeded) || got != (graph.HistoryBounds{}) {
		t.Fatal("lock wait ignored cancellation")
	}
}

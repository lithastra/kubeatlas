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

func TestStore_HistoryEvents(t *testing.T) {
	storetest.RunHistoryEvents(t, func(_ *testing.T) graph.GraphStore { return New() })
}

func TestStore_HistoryEventsCancelledLockWait(t *testing.T) {
	s := New()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	q := graph.HistoryEventQuery{From: time.Unix(1, 0), To: time.Now()}
	s.mu.Lock()
	finished := make(chan error, 1)
	go func() { _, err := s.ReadHistoryEvents(ctx, q); finished <- err }()
	select {
	case err := <-finished:
		s.mu.Unlock()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lock wait error=%v", err)
		}
	case <-time.After(time.Second):
		s.mu.Unlock()
		t.Fatal("history read ignored cancellation while waiting for writer")
	}
}

func TestStore_HistoryEventsLegacyPayloadAndConcurrentReaders(t *testing.T) {
	s := New()
	ctx := context.Background()
	now := time.Now()
	q := graph.HistoryEventQuery{ClusterID: "prod", From: now.Add(-time.Second), To: now.Add(time.Hour)}
	// Inject a synthetic old row directly, bypassing the current write sanitizer.
	s.events = []graph.ResourceEvent{{ID: 1, Timestamp: now, ClusterID: "prod", Kind: "Pod", Name: "one",
		Data: map[string]any{"must not inspect": func() { t.Error("payload was invoked") }}}}
	s.eventSeq = 1
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 50 {
			if err := s.AppendEvent(ctx, graph.ResourceEvent{Timestamp: now, ClusterID: "other", Kind: "Pod"}); err != nil {
				t.Error(err)
			}
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 50 {
				page, err := s.ReadHistoryEvents(ctx, q)
				if err != nil || len(page.Events) != 1 || page.Events[0].ID != 1 || page.Truncated {
					t.Errorf("concurrent read lost scope: %+v error=%v", page, err)
					return
				}
				page.Events[0].Name = "mutation"
			}
		})
	}
	wg.Wait()
}

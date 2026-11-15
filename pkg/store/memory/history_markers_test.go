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

func TestStoreHistoryMarkers(t *testing.T) {
	storetest.RunHistoryMarkers(t, func(*testing.T) graph.GraphStore { return New() })
}
func TestHistoryMarkersLockWaitCancellation(t *testing.T) {
	s := New()
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got, err := s.ReadHistoryMarkers(ctx, "prod")
	if !errors.Is(err, context.DeadlineExceeded) || got != nil {
		t.Fatal("lock wait ignored deadline")
	}
}

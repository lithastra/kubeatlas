// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
)

func TestSnapshotMarkerRejectsBeforeConnection(t *testing.T) {
	// No pool: rejection must happen before any database operation.
	s := &Store{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if m, err := s.RecordSnapshotMarker(ctx, "prod", graph.SnapshotTriggerManual); !errors.Is(err, context.Canceled) || m.ID != 0 {
		t.Fatal("cancelled request reached the pool", m, err)
	}
	if m, err := s.RecordSnapshotMarker(context.Background(), "bad:scope", graph.SnapshotTriggerManual); !errors.Is(err, graph.ErrHistoryEventQuery) || m.ID != 0 {
		t.Fatal("invalid scope reached the pool", m, err)
	}
}

func TestStoreSnapshotMarker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping disposable PostgreSQL+AGE integration tests in -short mode")
	}
	h := StartPostgresWithAGE(t)
	ctx := context.Background()
	s, err := New(ctx, Config{DSN: h.ConnStr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	storetest.RunSnapshotMarker(t, func(t *testing.T) graph.GraphStore {
		if err := s.truncateAll(ctx); err != nil {
			t.Fatal(err)
		}
		return s
	})
}

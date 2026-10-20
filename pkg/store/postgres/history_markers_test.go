// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package postgres

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
)

func TestStoreHistoryMarkers(t *testing.T) {
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
	storetest.RunHistoryMarkers(t, func(t *testing.T) graph.GraphStore {
		if err := s.truncateAll(ctx); err != nil {
			t.Fatal(err)
		}
		return s
	})
}

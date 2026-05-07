// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
)

// TestStore_Contract runs the shared GraphStore contract suite against
// the Postgres skeleton plus a small set of tier-2-specific subtests.
// A single container is shared across all subtests (~13 today);
// truncateAll between contract cases gives each one a clean slate
// without paying for a fresh container every time.
func TestStore_Contract(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping testcontainers test in -short mode")
	}

	h := StartPostgresWithAGE(t)

	ctx := context.Background()
	store, err := New(ctx, Config{DSN: h.ConnStr})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(store.Close)

	t.Run("contract", func(t *testing.T) {
		storetest.Run(t, func(t *testing.T) graph.GraphStore {
			t.Helper()
			if err := store.truncateAll(ctx); err != nil {
				t.Fatalf("truncateAll: %v", err)
			}
			return store
		})
	})

	// Init is idempotent: a second call must not fail or duplicate
	// schema (ON CONFLICT-style guards already exist in DDL).
	t.Run("init is idempotent", func(t *testing.T) {
		if err := store.Init(ctx); err != nil {
			t.Fatalf("second Init: %v", err)
		}
	})

	// New with a custom MaxConns covers the non-default pool-sizing
	// branch in postgres.New.
	t.Run("custom MaxConns", func(t *testing.T) {
		s2, err := New(ctx, Config{DSN: h.ConnStr, MaxConns: 5})
		if err != nil {
			t.Fatalf("New with MaxConns=5: %v", err)
		}
		t.Cleanup(s2.Close)
	})
}

// TestNew_BadDSN guards against silently returning a half-initialized
// Store when the DSN is empty.
func TestNew_BadDSN(t *testing.T) {
	_, err := New(context.Background(), Config{})
	if err == nil {
		t.Fatal("expected error for empty DSN, got nil")
	}
}

// TestNew_BadDSNFormat covers the DSN-parse error branch without
// needing a container (pgxpool.ParseConfig rejects malformed URIs).
func TestNew_BadDSNFormat(t *testing.T) {
	_, err := New(context.Background(), Config{DSN: "://not a valid dsn"})
	if err == nil {
		t.Fatal("expected DSN parse error, got nil")
	}
}

// TestNew_UnreachableHost covers the connect-failure branch in
// postgres.New: a syntactically valid DSN that points at a port no PG
// is listening on. Uses a short context so the test does not hang on
// TCP timeouts.
func TestNew_UnreachableHost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// 127.0.0.1:1 — RFC 6335 reserved port, nothing should listen.
	_, err := New(ctx, Config{DSN: "postgres://kubeatlas:kubeatlas@127.0.0.1:1/kubeatlas?sslmode=disable&connect_timeout=1"})
	if err == nil {
		t.Fatal("expected connect error, got nil")
	}
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// TestNew_DefaultIsMemory: the zero-value Config must return the
// in-memory implementation. This pins the "zero-config Tier 1"
// promise (guide §2.3).
func TestNew_DefaultIsMemory(t *testing.T) {
	s, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := s.(*memory.Store); !ok {
		t.Errorf("zero-value Config: got %T, want *memory.Store", s)
	}
}

// TestNew_ExplicitMemory matches BackendMemory by name.
func TestNew_ExplicitMemory(t *testing.T) {
	s, err := New(context.Background(), Config{Backend: BackendMemory})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := s.(*memory.Store); !ok {
		t.Errorf("BackendMemory: got %T, want *memory.Store", s)
	}
}

// TestNew_PostgresRequiresDSN: backend=postgres with empty Postgres.DSN
// must fail at the factory boundary, before any pgxpool round-trip.
func TestNew_PostgresRequiresDSN(t *testing.T) {
	_, err := New(context.Background(), Config{Backend: BackendPostgres})
	if err == nil {
		t.Fatal("expected error for postgres without DSN, got nil")
	}
	if !strings.Contains(err.Error(), "DSN") {
		t.Errorf("error %q does not mention DSN", err)
	}
}

// TestNew_UnknownBackend rejects typos rather than silently picking a
// default.
func TestNew_UnknownBackend(t *testing.T) {
	_, err := New(context.Background(), Config{Backend: "neo4j"})
	if err == nil {
		t.Fatal("expected error for unknown backend, got nil")
	}
}

// The postgres backend's end-to-end path is exercised by the
// contract suite in pkg/store/postgres (TestStore_Contract). The
// factory's only job is dispatch, which the four unit tests above
// fully cover without requiring Docker here.

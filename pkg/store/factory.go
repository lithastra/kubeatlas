// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package store is the entry point for graph.GraphStore construction.
// Callers (cmd/kubeatlas, tests, future binaries) go through New so
// the backend choice — Tier 1 in-memory or Tier 2 PostgreSQL+AGE —
// is wired at startup from a single Config and never mutated at
// runtime (guide §2.3, anti-pattern #10: backend selection is
// startup-time only, no hot-swap).
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"github.com/lithastra/kubeatlas/pkg/store/postgres"
)

// Backend names the persistence implementation. The empty value is
// treated as BackendMemory so a zero-value Config gives the
// zero-config Tier 1 path the README promises.
type Backend string

const (
	BackendMemory   Backend = "memory"
	BackendPostgres Backend = "postgres"
)

// Config carries the inputs required to construct a GraphStore. Add
// new fields by appending — never reordering or renaming — so older
// callers keep compiling (guide §2.1: zero churn on store-config
// surface during v1.x).
type Config struct {
	Backend  Backend
	Postgres postgres.Config
}

// New constructs a GraphStore backed by the configured implementation.
// On error the caller gets a non-nil error and a nil store; partial
// state (e.g. an open pgx pool that survived a downstream failure)
// is cleaned up before return.
func New(ctx context.Context, cfg Config) (graph.GraphStore, error) {
	switch cfg.Backend {
	case "", BackendMemory:
		return memory.New(), nil
	case BackendPostgres:
		if cfg.Postgres.DSN == "" {
			return nil, errors.New("store.New: backend=postgres requires Postgres.DSN")
		}
		return postgres.New(ctx, cfg.Postgres)
	default:
		return nil, fmt.Errorf("store.New: unknown backend %q", cfg.Backend)
	}
}

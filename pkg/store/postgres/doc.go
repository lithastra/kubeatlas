// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package postgres is the Tier 2 implementation of graph.GraphStore
// backed by PostgreSQL. It is opt-in (Helm persistence.enabled=true);
// the default deployment continues to use the in-memory store in
// pkg/store/memory.
//
// Phase 2 ships the Tier 2 backend in two halves:
//
//   - P2-T2 (this skeleton) stores resources and edges in plain
//     PostgreSQL tables: resources(id, data jsonb), edges(from_id,
//     to_id, type). All GraphStore methods use ordinary SQL; this is
//     the correctness baseline that lets us layer AGE on top.
//   - P2-T3 adds Apache AGE: graph creation, vertex/edge labels, the
//     migration framework, and the openCypher path used by traversal
//     queries (BlastRadius, etc.).
//
// CGO INVARIANT (guide §2.2): this package depends on jackc/pgx/v5,
// which is pure Go. CGO_ENABLED=0 must always build. Do not introduce
// lib/pq or any cgo-linked driver.
package postgres

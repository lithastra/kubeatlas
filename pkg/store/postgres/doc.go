// Package postgres will provide the Tier 2 implementation of the
// graph.GraphStore interface backed by PostgreSQL with the Apache AGE
// extension for native graph queries.
//
// This package is a placeholder during Phase 0 / Phase 1. It is
// enabled in v1.0 to support multi-replica deployments and
// persistent graph history. Until then the Tier 1 in-memory store
// in pkg/store/memory is the only available backend.
package postgres

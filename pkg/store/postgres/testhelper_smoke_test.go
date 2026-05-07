// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"strings"
	"testing"
)

// TestStartPostgresWithAGE is the trivial sanity check for the
// testcontainers helper introduced in P2-T1: the container starts,
// the AGE extension is callable, and ag_catalog.ag_graph is
// queryable in the same session that issued LOAD 'age'.
func TestStartPostgresWithAGE(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping testcontainers test in -short mode")
	}

	h := StartPostgresWithAGE(t)

	if h.ConnStr == "" {
		t.Fatal("expected non-empty connection string")
	}
	if !strings.HasPrefix(h.ConnStr, "postgres://") {
		t.Errorf("ConnStr=%q, want postgres:// scheme", h.ConnStr)
	}

	// Verifying AGE is reachable end-to-end: LOAD 'age' is required
	// per session, and ag_catalog.ag_graph is the canonical AGE
	// metadata table. A successful SELECT proves the extension is
	// installed and the search_path resolves ag_catalog.
	if err := execPSQL(context.Background(), h.Container,
		`LOAD 'age'; SET search_path = ag_catalog, "$user", public; SELECT count(*) FROM ag_catalog.ag_graph`,
	); err != nil {
		t.Fatalf("AGE smoke query: %v", err)
	}
}

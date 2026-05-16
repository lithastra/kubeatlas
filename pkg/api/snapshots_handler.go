// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// SnapshotListResponse is the body of GET /api/v1/snapshots — the
// recorded full-sync markers, most-recent first.
type SnapshotListResponse struct {
	Snapshots []graph.SnapshotMeta `json:"snapshots"`
	Count     int                  `json:"count"`
}

// snapshotsUnavailable writes the 503 every F-111 snapshot handler
// returns when the feature is not active. Snapshots are Tier 2 only
// (invariant 2.2); main.go calls WithSnapshots solely on a Tier 2
// install with snapshots.enabled, so s.snapshotsEnabled==false
// means "Tier 1, or snapshots not switched on".
func snapshotsUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, CodeUnavailable,
		"snapshots require Tier 2 (PostgreSQL) with snapshots.enabled=true")
}

// handleSnapshots serves GET /api/v1/snapshots.
func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	if !s.snapshotsEnabled {
		snapshotsUnavailable(w)
		return
	}
	metas, err := s.store.ListSnapshotMeta(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if metas == nil {
		metas = []graph.SnapshotMeta{}
	}
	writeJSON(w, http.StatusOK, SnapshotListResponse{
		Snapshots: metas,
		Count:     len(metas),
	})
}

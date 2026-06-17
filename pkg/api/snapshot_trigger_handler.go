// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// SnapshotTriggerResponse is the body of
// POST /api/_internal/snapshot/trigger — the periodic full-sync
// marker the F-111 CronJob (and `kubeatlas snapshot trigger`)
// writes. The counts echo what was recorded in the snapshot_meta
// row so the caller's log shows the cluster size at trigger time.
type SnapshotTriggerResponse struct {
	Trigger       string `json:"trigger"`
	ResourceCount int64  `json:"resourceCount"`
	EdgeCount     int64  `json:"edgeCount"`
	DurationMS    int64  `json:"durationMs"`
}

// handleSnapshotTrigger serves POST /api/_internal/snapshot/trigger.
//
// It records one snapshot_meta row anchoring the diff endpoint to a
// known full-sync point. Resource and edge totals come from the
// P3-T0a pushdown queries (CountKindsByNamespace +
// CountCrossNamespaceEdges) — NOT store.Snapshot, which would
// materialise the whole graph just to count it (the OOM the
// pushdown work fixed).
//
// The endpoint lives under /api/_internal — it is served only on
// the ClusterIP Service, never exposed through Ingress. The Helm
// CronJob is the intended caller.
func (s *Server) handleSnapshotTrigger(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// trigger kind: "periodic" (CronJob) or "manual" (an operator
	// running `kubeatlas snapshot trigger`). Empty defaults to
	// manual; anything else is a client error.
	trigger := graph.SnapshotTrigger(r.URL.Query().Get("trigger"))
	switch trigger {
	case "":
		trigger = graph.SnapshotTriggerManual
	case graph.SnapshotTriggerPeriodic, graph.SnapshotTriggerManual:
		// accepted
	default:
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"trigger must be 'periodic' or 'manual'")
		return
	}

	kinds, err := s.store.CountKindsByNamespace(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	var resourceCount int64
	for _, byKind := range kinds {
		for _, n := range byKind {
			resourceCount += int64(n)
		}
	}

	edges, err := s.store.CountCrossNamespaceEdges(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	var edgeCount int64
	for _, n := range edges {
		edgeCount += int64(n)
	}

	durationMS := time.Since(start).Milliseconds()
	if err := s.store.AppendSnapshotMeta(r.Context(), graph.SnapshotMeta{
		ResourceCount: resourceCount,
		EdgeCount:     edgeCount,
		DurationMS:    durationMS,
		Trigger:       trigger,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, SnapshotTriggerResponse{
		Trigger:       string(trigger),
		ResourceCount: resourceCount,
		EdgeCount:     edgeCount,
		DurationMS:    durationMS,
	})
}

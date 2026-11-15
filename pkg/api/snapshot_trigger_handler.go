// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// SnapshotTriggerResponse is the body of
// POST /api/_internal/snapshot/trigger. Counts echo one atomic observation of
// the selected stored graph, not a full sync, backup, or history completeness.
type SnapshotTriggerResponse struct {
	ClusterID     string `json:"clusterId,omitempty"`
	Trigger       string `json:"trigger"`
	ResourceCount int64  `json:"resourceCount"`
	EdgeCount     int64  `json:"edgeCount"`
	DurationMS    int64  `json:"durationMs"`
}

// handleSnapshotTrigger records one scoped observation marker. Internal
// operator access remains a deployment boundary; read-side RBAC is only an
// additional visibility check, not a grant of write/administrator authority.
// Counts and append share one store snapshot/critical section. The handler never
// falls back to global counters, retries, or asserts history/recovery completeness.
func (s *Server) handleSnapshotTrigger(w http.ResponseWriter, r *http.Request) {
	if !s.snapshotsEnabled {
		snapshotsUnavailable(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), graph.HistoryReadTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Authorization")
	select {
	case s.impactSem <- struct{}{}:
		defer func() { <-s.impactSem }()
	default:
		writeImpactReply(w, ctx, impactFailure(http.StatusTooManyRequests, CodeTooManyRequests, "analysis capacity is busy"))
		return
	}
	cluster, failure := s.historyScope(r)
	if failure.status != 0 {
		writeImpactReply(w, ctx, failure)
		return
	}
	q := r.URL.Query()
	if len(q["trigger"]) > 1 {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "select one trigger kind")
		return
	}
	trigger := graph.SnapshotTrigger(q.Get("trigger"))
	if trigger == "" {
		trigger = graph.SnapshotTriggerManual
	}
	if !graph.ValidSnapshotMarker(cluster, trigger) {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "trigger must be periodic or manual")
		return
	}
	recorder, ok := s.store.(graph.SnapshotMarkerRecorder)
	if !ok {
		writeImpactReply(w, ctx, historyReadFailure(errHistoryUnsupported))
		return
	}
	if err := ctx.Err(); err != nil {
		writeImpactReply(w, ctx, historyReadFailure(err))
		return
	}
	marker, err := recorder.RecordSnapshotMarker(ctx, cluster, trigger)
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		writeError(w, http.StatusGatewayTimeout, "deadline_exceeded", "snapshot marker outcome may be unknown; no automatic retry")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "snapshot marker outcome may be unknown; no automatic retry")
		return
	}
	if marker.ClusterID != cluster || marker.Trigger != trigger || marker.ID < 1 || marker.Timestamp.IsZero() || marker.ResourceCount < 0 || marker.EdgeCount < 0 || marker.DurationMS < 0 {
		writeError(w, http.StatusInternalServerError, CodeInternal, "snapshot marker receipt is invalid; no automatic retry")
		return
	}
	writeJSON(w, http.StatusOK, SnapshotTriggerResponse{ClusterID: cluster, Trigger: string(trigger), ResourceCount: marker.ResourceCount, EdgeCount: marker.EdgeCount, DurationMS: marker.DurationMS})
}

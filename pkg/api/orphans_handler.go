// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

// OrphansResponse is the body of GET /api/v1alpha1/orphans.
//
// Reports lists every resource that is either an orphan (non-top-
// level kind with zero incoming edges) or a standalone Pod (no
// OwnerReference). Count is provided so dashboards don't need to
// length-check client-side.
type OrphansResponse struct {
	Reports []analysis.OrphanReport `json:"reports"`
	Count   int                     `json:"count"`
}

// handleOrphans serves GET /api/v1alpha1/orphans.
//
// Optional query param: namespace=<ns>. Empty namespace = whole-
// cluster sweep. The detector returns a stable order so successive
// calls diff cleanly.
func (s *Server) handleOrphans(w http.ResponseWriter, r *http.Request) {
	opts := analysis.OrphanOptions{
		Namespace: r.URL.Query().Get("namespace"),
	}
	reports, err := analysis.DetectOrphans(r.Context(), s.store, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if reports == nil {
		reports = []analysis.OrphanReport{}
	}
	writeJSON(w, http.StatusOK, OrphansResponse{Reports: reports, Count: len(reports)})
}

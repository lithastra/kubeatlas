// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

// CyclesResponse is the body of GET /api/v1alpha1/cycles. Cycles
// is the list of strongly connected components with size > 1;
// Count is provided so dashboards don't need to length-check the
// array client-side.
type CyclesResponse struct {
	Cycles []analysis.CycleReport `json:"cycles"`
	Count  int                    `json:"count"`
}

// handleCycles serves GET /api/v1alpha1/cycles.
//
// In a healthy cluster the response is { "cycles": [], "count": 0 }.
// Anything non-empty is a strong "investigate this" signal — see
// pkg/graph/analysis/cycles.go for what shows up here in practice.
func (s *Server) handleCycles(w http.ResponseWriter, r *http.Request) {
	cycles, err := analysis.DetectCycles(r.Context(), s.store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if cycles == nil {
		cycles = []analysis.CycleReport{}
	}
	writeJSON(w, http.StatusOK, CyclesResponse{Cycles: cycles, Count: len(cycles)})
}

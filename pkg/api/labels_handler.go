// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// LabelsResponse is the body of GET /api/v1/labels — every label key
// in the cluster with its most common values. It is the data the
// UI's F-114 "group by label" picker is built from.
type LabelsResponse struct {
	Labels []graph.LabelStat `json:"labels"`
	Count  int               `json:"count"`
}

// handleLabels serves GET /api/v1/labels.
//
// Each key's value list is capped at graph.MaxLabelValuesPerKey; the
// store does the cap so a high-cardinality key (pod-template-hash)
// cannot blow the response up. LabelStat.ValueCount still reports the
// true distinct-value total.
func (s *Server) handleLabels(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.LabelStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if stats == nil {
		stats = []graph.LabelStat{}
	}
	writeJSON(w, http.StatusOK, LabelsResponse{Labels: stats, Count: len(stats)})
}

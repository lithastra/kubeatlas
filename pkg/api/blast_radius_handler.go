// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

// BlastRadiusResponse is the body of GET
// /api/v1alpha1/blast-radius/{ns}/{kind}/{name}.
//
// Source identifies the resource the query was rooted at; Affected
// is the transitive set walked along incoming edges. Count is
// pre-computed so dashboards don't have to len() the array
// client-side.
type BlastRadiusResponse struct {
	Source   graph.Resource   `json:"source"`
	Affected []graph.Resource `json:"affected"`
	Count    int              `json:"count"`
	MaxDepth int              `json:"maxDepth"`
}

func (s *Server) handleBlastRadius(w http.ResponseWriter, r *http.Request) {
	ns, kind, name, ok := pathParts(r)
	if !ok {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "URL must be /blast-radius/{namespace}/{kind}/{name}")
		return
	}
	id := makeID(ns, kind, name)

	// Confirm the source exists before traversing — a 404 here is
	// far more useful than an empty 200 for "no such resource".
	src, err := s.store.GetResource(r.Context(), id)
	if err != nil {
		writeNotFoundOr500(w, err, id)
		return
	}

	depth := 5
	if raw := r.URL.Query().Get("max_depth"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "max_depth must be a positive integer")
			return
		}
		depth = n
	}

	var edgeTypes []graph.EdgeType
	if raw := r.URL.Query().Get("edge_types"); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			edgeTypes = append(edgeTypes, graph.EdgeType(t))
		}
	}

	includeSource := false
	if raw := r.URL.Query().Get("include_source"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "include_source must be a boolean")
			return
		}
		includeSource = v
	}

	affected, err := analysis.BlastRadius(r.Context(), s.store, id, analysis.Options{
		MaxDepth:      depth,
		EdgeTypes:     edgeTypes,
		IncludeSource: includeSource,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if affected == nil {
		affected = []graph.Resource{}
	}

	writeJSON(w, http.StatusOK, BlastRadiusResponse{
		Source:   src,
		Affected: affected,
		Count:    len(affected),
		MaxDepth: depth,
	})
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

const (
	// exportConcurrency is how many /export renders may run at
	// once. graphviz is CPU- and memory-bound, so excess callers
	// are shed with 429 rather than piling up dot processes.
	exportConcurrency = 2

	// exportMaxNodes caps the size of a renderable view. Past this
	// a dot layout is both slow and unreadable; the caller is told
	// to narrow the scope with ?namespace=.
	exportMaxNodes = 1000
)

// handleExport serves GET /api/v1alpha1/export — a server-side
// render of the dependency graph as an SVG or PNG image (ADR 0012).
//
// Query params:
//   - format:    "svg" (default) or "png".
//   - namespace: optional; empty renders the whole cluster.
//
// The success response body is the raw image, not JSON. Guards:
//   - 400 when format is neither svg nor png;
//   - 429 when exportConcurrency renders are already in flight;
//   - 413 when the view exceeds exportMaxNodes;
//   - 503 when the graphviz `dot` binary is absent.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "svg"
	}
	if format != "svg" && format != "png" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			"format must be 'svg' or 'png'")
		return
	}
	namespace := r.URL.Query().Get("namespace")

	// Concurrency guard: graphviz is CPU/memory-bound, so cap the
	// number of in-flight renders and shed the rest immediately.
	select {
	case s.exportSem <- struct{}{}:
		defer func() { <-s.exportSem }()
	default:
		writeError(w, http.StatusTooManyRequests, CodeTooManyRequests,
			"too many export renders in flight; retry shortly")
		return
	}

	g, err := s.store.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}

	// Node cap: refuse a view too large to lay out usefully.
	nodes := 0
	for _, res := range g.Resources {
		if namespace == "" || res.Namespace == namespace {
			nodes++
		}
	}
	if nodes > exportMaxNodes {
		writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
			fmt.Sprintf("view has %d nodes, over the %d-node export limit; "+
				"narrow it with ?namespace=", nodes, exportMaxNodes))
		return
	}

	img, err := graph.ToImage(r.Context(), g, format, graph.DOTOptions{Namespace: namespace})
	if err != nil {
		if errors.Is(err, graph.ErrGraphvizNotFound) {
			writeError(w, http.StatusServiceUnavailable, CodeUnavailable,
				"server-side render requires the graphviz 'dot' binary, which "+
					"is missing from this deployment; install the graphviz "+
					"package or use an official KubeAtlas image")
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}

	contentType := "image/svg+xml"
	if format == "png" {
		contentType = "image/png"
	}
	scope := namespace
	if scope == "" {
		scope = "cluster"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("inline; filename=%q", "kubeatlas-"+scope+"."+format))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img)
}

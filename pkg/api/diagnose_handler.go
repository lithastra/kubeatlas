// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/version"
)

// handleDiagnose serves GET /api/v1/diagnose — the server-side F-301
// diagnostic report. It is the in-cluster equivalent of the
// `kubeatlas diagnose` CLI: same data, generated from the running
// server's store instead of an offline scan.
//
// Query params:
//   - namespace: restrict to one namespace; empty = whole cluster.
//   - format:    "json" (default) or "html".
func (s *Server) handleDiagnose(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns := q.Get("namespace")

	format := q.Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "html" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "format must be 'json' or 'html'")
		return
	}

	scope := analysis.DiagnoseScope{Namespace: ns, AllNamespaces: ns == ""}
	report, err := analysis.GenerateReport(r.Context(), s.store, scope, version.Version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}

	if format == "html" {
		html, err := analysis.RenderHTML(r.Context(), report)
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(html)
		return
	}

	writeJSON(w, http.StatusOK, report)
}

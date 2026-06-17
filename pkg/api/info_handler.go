// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/lithastra/kubeatlas/pkg/version"
)

// InfoResponse is the body of GET /api/v1/info — a small, additive
// build/runtime descriptor for operators and tooling.
//
// GraphStoreVersion is the INTERNAL GraphStore interface version (the
// store's StoreVersion()), e.g. "v2". It is deliberately distinct from
// the product/release version and from the HTTP API versions
// (v1alpha1 / v1): bumping the store interface carries no public-API
// or release-version meaning.
type InfoResponse struct {
	Version           string `json:"version"`
	Commit            string `json:"commit"`
	BuildDate         string `json:"build_date"`
	GraphStoreVersion string `json:"graphstore_version"`
}

// handleInfo returns build metadata plus the internal GraphStore
// interface version. v1-only — it is not part of the frozen v1alpha1
// surface.
func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, InfoResponse{
		Version:           version.Version,
		Commit:            version.Commit,
		BuildDate:         version.Date,
		GraphStoreVersion: s.store.StoreVersion(),
	})
}

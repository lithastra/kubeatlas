// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
)

// federationClustersResponse is the body served by
// GET /api/v1/federation/clusters (P3-T22). Empty Clusters with
// Mode="single" tells the UI to hide the cluster switcher entirely.
type federationClustersResponse struct {
	// Mode is "federated" when a multicluster.Manager is wired and at
	// least one cluster is attached, "single" otherwise. The UI uses
	// this to decide whether to render the cluster switcher.
	Mode string `json:"mode"`

	// Clusters lists every attached member cluster, sorted. Empty in
	// single mode.
	Clusters []string `json:"clusters"`
}

// handleFederationClusters serves GET /api/v1/federation/clusters.
func (s *Server) handleFederationClusters(w http.ResponseWriter, _ *http.Request) {
	resp := federationClustersResponse{Mode: "single"}
	if s.clusterLister != nil {
		clusters := s.clusterLister.ListClusters()
		if len(clusters) > 0 {
			resp.Mode = "federated"
			resp.Clusters = clusters
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleFederationGraph serves GET /api/v1/federation/graph?cluster=a,b.
// The cluster query parameter is required; it accepts a single
// comma-separated value, repeated values, or both.
//
// Federation is only meaningful when a multicluster.Manager is wired;
// a request without that returns 503 so the UI knows to fall back to
// the single-cluster /api/v1/graph?level=cluster surface.
func (s *Server) handleFederationGraph(w http.ResponseWriter, r *http.Request) {
	if s.clusterLister == nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			"federation is not enabled on this server (multicluster.enabled=false)")
		return
	}

	clusters := parseClusterQuery(r.URL.Query()["cluster"])
	if len(clusters) == 0 {
		writeJSONError(w, http.StatusBadRequest,
			"cluster query parameter is required (e.g. ?cluster=prod,staging)")
		return
	}

	// Validate every requested cluster is attached. We do not silently
	// drop unknowns — the UI would render the cluster switcher with a
	// member selected that returns no data, which is a confusing failure
	// mode worth a hard error.
	attached := make(map[string]struct{})
	for _, c := range s.clusterLister.ListClusters() {
		attached[c] = struct{}{}
	}
	var missing []string
	for _, c := range clusters {
		if _, ok := attached[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		writeJSONError(w, http.StatusBadRequest,
			"unknown cluster(s): "+strings.Join(missing, ", "))
		return
	}

	view, err := aggregator.MergeClusters(r.Context(), s.store, clusters)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(view)
}

// parseClusterQuery accepts the cluster= form in both shapes — a
// single comma-separated value (?cluster=a,b) and repeated values
// (?cluster=a&cluster=b) — and merges them, trimming whitespace and
// dropping empties.
func parseClusterQuery(raw []string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, v := range raw {
		for _, item := range strings.Split(v, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

// writeJSONError emits {"error": "..."} with the given status. Shared
// with the existing v1 error pattern — see handlers.go.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

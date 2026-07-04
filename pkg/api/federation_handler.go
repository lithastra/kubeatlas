// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"strconv"
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
//
// When F-206 RBAC is active the listed clusters are filtered to those
// the request's bearer token may see; an unauthenticated / unauthorised
// caller is rejected (401/403) rather than shown an empty list, so it
// can tell "denied" from "no clusters attached".
func (s *Server) handleFederationClusters(w http.ResponseWriter, r *http.Request) {
	resp := federationClustersResponse{Mode: "single"}
	if s.clusterLister != nil {
		allow, status := s.clusterVisibility(r)
		if status != 0 {
			writeClusterAuthError(w, status)
			return
		}
		clusters := s.clusterLister.ListClusters()
		if allow != nil {
			clusters = filterAllowedClusters(clusters, allow)
		}
		if len(clusters) > 0 {
			resp.Mode = "federated"
			resp.Clusters = clusters
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// clusterVisibility resolves the request's allowed cluster set (F-206).
// Returns (allow, 0) when authorised — a nil allow meaning unrestricted
// (RBAC off) — or (nil, 401|403) when RBAC is on and the caller is
// unauthenticated / unauthorised. A nil rbac is always unrestricted.
func (s *Server) clusterVisibility(r *http.Request) (map[string]struct{}, int) {
	if s.rbac == nil {
		return nil, 0
	}
	return s.rbac.VisibleClusters(r)
}

// filterAllowedClusters keeps only the clusters present in allow,
// preserving order.
func filterAllowedClusters(all []string, allow map[string]struct{}) []string {
	out := make([]string, 0, len(all))
	for _, c := range all {
		if _, ok := allow[c]; ok {
			out = append(out, c)
		}
	}
	return out
}

// writeClusterAuthError writes the 401/403 an RBAC-denied cluster
// request gets — never a 200 with an empty body (invariant 2.4).
func writeClusterAuthError(w http.ResponseWriter, status int) {
	msg := "not authorised for the requested cluster(s)"
	if status == http.StatusUnauthorized {
		msg = "a bearer token is required for cluster access"
	}
	writeJSONError(w, status, msg)
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

	// F-206: resolve the caller's cluster visibility up front. An
	// unauthenticated / unauthorised caller is rejected (401/403) before
	// any store work.
	allow, status := s.clusterVisibility(r)
	if status != 0 {
		writeClusterAuthError(w, status)
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

	// F-206: an authorised caller may still request a cluster outside
	// its allow-set. Reject with 403 rather than silently dropping it —
	// the caller must know it was denied, not just shown less. A nil
	// allow-set means RBAC is off (unrestricted).
	if allow != nil {
		var forbidden []string
		for _, c := range clusters {
			if _, ok := allow[c]; !ok {
				forbidden = append(forbidden, c)
			}
		}
		if len(forbidden) > 0 {
			writeJSONError(w, http.StatusForbidden,
				"not authorised for cluster(s): "+strings.Join(forbidden, ", "))
			return
		}
	}

	level := strings.TrimSpace(r.URL.Query().Get("level"))
	if level == "" {
		level = "resource"
	}
	var view *aggregator.FederatedView
	var err error
	switch level {
	case "resource":
		view, err = aggregator.MergeClusters(r.Context(), s.store, clusters)
	case "cluster":
		view, err = aggregator.MergeClustersAtClusterLevel(r.Context(), s.store, clusters)
	default:
		writeJSONError(w, http.StatusBadRequest,
			"unknown level "+strconv.Quote(level)+" (want 'resource' or 'cluster')")
		return
	}
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

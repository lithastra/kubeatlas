// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"strings"
)

// API version constants. v1alpha1 is the original Phase 1 path
// group; v1 is the GA version that landed in Phase 2 P2-T22.
//
// Both versions are served by the same handlers, hitting the same
// store query path. The only difference is serialisation: v1
// exposes a handful of enrichment fields (blast_radius_count,
// is_orphan, in_cycle, has_rego_edges) that v1alpha1 omits, so
// scripts pinned to v1alpha1 keep their byte-shape across the
// upgrade. The v1alpha1 surface is frozen — nothing may be
// removed or renamed; CI's api-compat-check enforces that.
const (
	APIVersionV1Alpha1 = "v1alpha1"
	APIVersionV1       = "v1"

	apiPrefixV1Alpha1 = "/api/v1alpha1/"
	apiPrefixV1       = "/api/v1/"
)

// apiVersionFor reports which API version the request hit. The
// path prefix is the source of truth — server.registerRoutes
// installs each handler under both /api/v1alpha1/<...> and
// /api/v1/<...>, so a handler that wants to branch on version
// just calls this. Defaults to v1alpha1 (the conservative pick
// for any non-API request that somehow ends up in a handler).
func apiVersionFor(r *http.Request) string {
	switch {
	case strings.HasPrefix(r.URL.Path, apiPrefixV1):
		return APIVersionV1
	case strings.HasPrefix(r.URL.Path, apiPrefixV1Alpha1):
		return APIVersionV1Alpha1
	default:
		return APIVersionV1Alpha1
	}
}

// versionedPattern flips a route's canonical Pattern (which is
// always written in v1alpha1 form in routes.go) to the v1 form.
// Any pattern that does not start with the v1alpha1 prefix is
// returned unchanged — that's how /healthz, /readyz, /metrics
// stay unversioned.
func versionedPattern(pattern, version string) string {
	if version == APIVersionV1 && strings.HasPrefix(pattern, apiPrefixV1Alpha1) {
		return apiPrefixV1 + strings.TrimPrefix(pattern, apiPrefixV1Alpha1)
	}
	return pattern
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// TestInfoHandler pins the v1-only GET /api/v1/info contract: it
// reports the internal GraphStore interface version ("v2") and is not
// served on the frozen v1alpha1 surface.
func TestInfoHandler(t *testing.T) {
	base, _, cleanup := seedAndServe(t, nil)
	defer cleanup()

	var info struct {
		Version           string `json:"version"`
		Commit            string `json:"commit"`
		BuildDate         string `json:"build_date"`
		GraphStoreVersion string `json:"graphstore_version"`
	}
	resp, body := getJSON(t, base+"/api/v1/info", &info)
	if resp.StatusCode != 200 {
		t.Fatalf("status code = %d (%s)", resp.StatusCode, body)
	}
	if info.GraphStoreVersion != graph.StoreInterfaceVersion {
		t.Errorf("graphstore_version = %q, want %q", info.GraphStoreVersion, graph.StoreInterfaceVersion)
	}
	if info.GraphStoreVersion != "v2" {
		t.Errorf("graphstore_version = %q, want \"v2\"", info.GraphStoreVersion)
	}

	// v1alpha1 must NOT serve /info — it is frozen (invariant 2.2).
	resp, _ = getJSON(t, base+"/api/v1alpha1/info", nil)
	if resp.StatusCode != 404 {
		t.Errorf("GET /api/v1alpha1/info status = %d, want 404 (v1alpha1 is frozen)", resp.StatusCode)
	}
}

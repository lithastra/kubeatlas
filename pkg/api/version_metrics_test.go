// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"strings"
	"testing"
)

// TestVersionMetrics_SplitsByAPIVersion sends an equal number of
// v1alpha1 and v1 requests and confirms /metrics reports them under
// separate counters, keyed by the (low-cardinality) endpoint label.
func TestVersionMetrics_SplitsByAPIVersion(t *testing.T) {
	base, _, cleanup := seedAndServe(t, petClinicSeed)
	defer cleanup()

	const n = 10
	for i := 0; i < n; i++ {
		getJSON(t, base+"/api/v1alpha1/graph?level=cluster", nil)
		getJSON(t, base+"/api/v1/graph?level=cluster", nil)
	}

	resp, body := getJSON(t, base+"/metrics", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("metrics status = %d", resp.StatusCode)
	}
	m := string(body)

	// The metric names must be present (and counter-typed) even before
	// any traffic; here they additionally carry the graph series.
	for _, want := range []string{
		"# TYPE kubeatlas_api_v1alpha1_requests_total counter",
		"# TYPE kubeatlas_api_v1_requests_total counter",
		`kubeatlas_api_v1alpha1_requests_total{endpoint="graph"} 10`,
		`kubeatlas_api_v1_requests_total{endpoint="graph"} 10`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics missing %q\n--- body ---\n%s", want, m)
		}
	}
}

// TestVersionMetrics_PresentAtZero confirms the v1alpha1 counter is
// scrapeable before any request lands (the retirement dashboard needs
// the series to exist from day one).
func TestVersionMetrics_PresentAtZero(t *testing.T) {
	base, _, cleanup := seedAndServe(t, nil)
	defer cleanup()
	_, body := getJSON(t, base+"/metrics", nil)
	if !strings.Contains(string(body), "kubeatlas_api_v1alpha1_requests_total") {
		t.Error("metrics missing kubeatlas_api_v1alpha1_requests_total at zero traffic")
	}
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package impact

import (
	"bytes"
	"encoding/json"
	"html"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

func TestReportSameCapturedJSONAndTextSafety(t *testing.T) {
	r := testResponse(t, testQuery())
	r.KubeAtlasVersion = "synthetic</pre><script>alert(1)</script>\x1b[31m\u202e"
	r.Analysis.Observation.Ordinary.Reasons = []string{"<img src=https://invalid.example/x onerror=alert(1)>"}
	jsonReport, err := Render(r, "json")
	if err != nil {
		t.Fatal(err)
	}
	htmlReport, err := Render(r, "html")
	if err != nil {
		t.Fatal(err)
	}
	textReport, err := Render(r, "text")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(htmlReport, []byte("<script")) || bytes.Contains(htmlReport, []byte("<img")) || bytes.Contains(htmlReport, []byte("<link")) || bytes.Contains(htmlReport, []byte("<iframe")) {
		t.Fatal("report injected executable markup or external resources")
	}
	if !bytes.Contains(htmlReport, []byte("default-src 'none'")) || !bytes.Contains(htmlReport, []byte("&lt;script")) ||
		bytes.Contains(textReport, []byte("\x1b")) || bytes.Contains(textReport, []byte("\u202e")) {
		t.Fatal("missing CSP/escaping or unsafe terminal controls")
	}
	_, captured, ok := strings.Cut(string(htmlReport), `<pre id="captured-json">`)
	if !ok {
		t.Fatal("missing captured JSON")
	}
	captured, _, _ = strings.Cut(captured, "</pre>")
	if html.UnescapeString(captured) != strings.TrimSuffix(string(jsonReport), "\n") {
		t.Fatal("HTML does not embed the identical JSON capture")
	}
	var decoded Response
	if err := json.Unmarshal(jsonReport, &decoded); err != nil {
		t.Fatal(err)
	}
	// time.Time's process-local monotonic clock/location is not a wire field.
	wantWire, _ := json.Marshal(r)
	gotWire, _ := json.Marshal(&decoded)
	if !bytes.Equal(wantWire, gotWire) {
		t.Fatal("JSON changed the captured wire response")
	}
	for _, expected := range []string{SharingWarning, "not effective permissions", "markers are not backups", "Direct: 1", "Indirect: 0", "Lower bound: false", "stored", "Resource version:", "unobserved endpoints"} {
		if !strings.Contains(string(textReport), expected) {
			t.Errorf("missing qualification %q", expected)
		}
	}
}

func TestReportEmptyAndRetentionQualifications(t *testing.T) {
	q := testQuery()
	q.Relation = analysis.ImpactDependencies
	r := testResponse(t, q)
	if r.Analysis.Counts.Total != 0 {
		t.Fatal("fixture should have no dependencies")
	}
	text, err := Render(r, "text")
	if err != nil || !strings.Contains(string(text), "No dependencies observed; analysis incomplete.") {
		t.Fatalf("empty coverage inflated: %v", err)
	}
	a := r.Analysis
	a.Observation.Ordinary.TypeScopeClosed = true
	a.Observation.Ordinary.StableWindow = true
	a.Observation.Ordinary.EmptyResultAssessment = "none_found_in_analyzed_scope"
	a.ModeledTraversalComplete = true
	text, _ = Render(r, "text")
	if !strings.Contains(string(text), "No dependencies found in the analyzed scope; not a safety guarantee.") {
		t.Fatal("closed scoped assessment missing")
	}
	a.Truncated = true
	a.Counts.LowerBound = true
	text, _ = Render(r, "text")
	if !strings.Contains(string(text), "analysis incomplete.") || !strings.Contains(string(text), "Lower bound: true") {
		t.Fatal("truncated zero incorrectly became absence")
	}
	a.Availability.Evidence.History.RetentionEvidence = &operations.HistoryRetentionEvidence{State: operations.RetentionObserved, Stale: true, Stopped: true}
	text, _ = Render(r, "text")
	for _, s := range []string{"Stale: true", "Stopped: true", "Observed first event: none observed / unknown", "do not prove continuous history"} {
		if !strings.Contains(string(text), s) {
			t.Errorf("missing retention qualifier %q", s)
		}
	}
}

func TestReportDoesNotMutateCapture(t *testing.T) {
	r := testResponse(t, testQuery())
	before, _ := json.Marshal(r)
	for _, format := range []string{"json", "html", "text"} {
		if _, err := Render(r, format); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := json.Marshal(r)
	if !bytes.Equal(before, after) {
		t.Fatal("rendering mutated a captured response")
	}
	if _, err := Render(r, "pdf"); err == nil {
		t.Fatal("unsupported output accepted")
	}
}

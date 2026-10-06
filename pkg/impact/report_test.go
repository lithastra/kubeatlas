// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package impact

import (
	"bytes"
	"encoding/json"
	"html"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
)

func TestReportCoverageDiagnostics(t *testing.T) {
	r := testResponse(t, testQuery())
	before := &r.Analysis.Observation.Ordinary.Before
	after := &r.Analysis.Observation.Ordinary.After
	checked := before.CapturedAt.Add(-time.Minute).UTC()
	before.Resources = []operations.ResourceCoverage{{Version: "v1", Resource: "pods", State: operations.CoveragePartial,
		Reasons: []operations.CoverageReason{operations.CoverageListDenied, operations.CoverageWatchDenied}}}
	after.Resources = []operations.ResourceCoverage{{Version: "v1", Resource: "pods", State: operations.CoverageObserved,
		Reasons: []operations.CoverageReason{}, InitialDeliveryDone: true, WatchEstablished: true}}
	after.Sources = []operations.CoverageSourceSnapshot{{Source: operations.CoverageSourceCRD, State: operations.CoveragePartial,
		InventoryLimited: true, Resources: []operations.ResourceCoverage{{Group: "example.test", Version: "v1", Resource: "widgets",
			State: operations.CoveragePartial, InitialDeliveryDone: true, WatchEstablished: true,
			Reasons: []operations.CoverageReason{operations.CoveragePersistenceFailed}}}}}
	before.APIInventory = &operations.APIInventoryEvidence{State: operations.APIInventoryComplete, CheckedAt: checked,
		Stale: true, Stopped: true, Resources: []operations.APIResourceDescriptor{{Version: "v1", Resource: "pods", Kind: "Pod"}}}
	after.APIInventory = &operations.APIInventoryEvidence{State: operations.APIInventoryPartial, CheckedAt: checked, Limited: true}
	capture, _ := json.Marshal(r)
	text, err := Render(r, "text")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Before required resource-type gaps:`,
		`"v1/pods": state="partial" initialDelivery=false watchEstablished=false reasons="list_permission_denied, watch_permission_denied"`,
		`After required resource-type gaps: none recorded in the supplied rows`,
		`After source "crd": state="partial" inventoryLimited=true registeredTypes=1`,
		`"example.test/v1/widgets": state="partial" initialDelivery=true watchEstablished=true reasons="persistence_gap"`,
		`Before API inventory: state="complete" stale=true stopped=true limited=false endpoints=1`,
		`After API inventory: state="partial" stale=false stopped=false limited=true endpoints=0`,
		`Checked: ` + checked.Format(time.RFC3339Nano),
		`API inventory describes advertised endpoints, not collector coverage or effective permissions.`,
		`Before API inventory: unavailable`,
	} {
		if !bytes.Contains(text, []byte(want)) {
			t.Errorf("missing captured coverage detail %q", want)
		}
	}
	if bytes.Contains(text, []byte(`"v1/pods": state="initialized_no_known_gap"`)) {
		t.Fatal("healthy row should not obscure the gap diagnostics")
	}
	htmlReport, err := Render(r, "html")
	if err != nil {
		t.Fatal(err)
	}
	_, summary, ok := strings.Cut(string(htmlReport), `<pre id="analysis-summary">`)
	if !ok {
		t.Fatal("missing readable HTML summary")
	}
	summary, _, _ = strings.Cut(summary, "</pre>")
	if html.UnescapeString(summary) != string(text) {
		t.Fatal("HTML summary must show the same captured coverage diagnostics")
	}
	gotJSON, _ := Render(r, "json")
	var compact bytes.Buffer
	if err := json.Compact(&compact, gotJSON); err != nil || !bytes.Equal(compact.Bytes(), capture) {
		t.Fatal("coverage presentation changed the JSON capture")
	}
}

func TestReportInventoryDoesNotQualifyEmptyResult(t *testing.T) {
	for _, state := range []operations.APIInventoryState{operations.APIInventoryUnknown, operations.APIInventoryComplete,
		operations.APIInventoryPartial, operations.APIInventoryDenied, operations.APIInventoryFailed} {
		t.Run(string(state), func(t *testing.T) {
			q := testQuery()
			q.Relation = analysis.ImpactDependencies
			r := testResponse(t, q)
			snapshot := &r.Analysis.Observation.Ordinary.After
			snapshot.Resources = nil
			snapshot.APIInventory = &operations.APIInventoryEvidence{State: state}
			if state != operations.APIInventoryUnknown {
				snapshot.APIInventory.CheckedAt = snapshot.CapturedAt.Add(-time.Minute)
			}
			text, err := Render(r, "text")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(text), `After API inventory: state="`+string(state)+`"`) ||
				!strings.Contains(string(text), "After required resource-type evidence: unavailable") ||
				!strings.Contains(string(text), "No dependencies observed; analysis incomplete.") ||
				strings.Contains(string(text), "No dependencies found in the analyzed scope") {
				t.Fatal("inventory metadata must not establish complete observation or absence of relationships")
			}
			if state == operations.APIInventoryUnknown && !strings.Contains(string(text), "Checked: none observed / unknown") {
				t.Fatal("unknown inventory check must not manufacture a timestamp")
			}
		})
	}
}

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

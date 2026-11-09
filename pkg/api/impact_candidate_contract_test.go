// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/impact"
	"github.com/lithastra/kubeatlas/pkg/multicluster"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/version"
)

const candidateTestCommit = "0123456789abcdef0123456789abcdef01234567"

func candidateTestSHA(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func TestImpactCandidatePlan(t *testing.T) {
	valid := `{
		"commit":"0123456789abcdef0123456789abcdef01234567","version":"1.7.0-rc.1","tier":"tier1",
		"cases":[{"name":"allowed","query":{"Namespace":"demo","Kind":"ConfigMap","Name":"settings",
		"ClusterID":"prod","Relation":"dependents","MaxDepth":5,"Limit":200},"status":200,
		"paths":{"prod:other/Pod/consumer":["USES_CONFIGMAP"]}}],
		"performance":{"case":"allowed","shape":"baseline-5k","resources":5000,"edges":6000,"host":"recorded host"}
	}`
	if _, err := readImpactCandidatePlan(strings.NewReader(valid), candidateTestSHA([]byte(valid))); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		valid + " null",
		strings.Replace(valid, `"paths":{"prod:other/Pod/consumer":["USES_CONFIGMAP"]}`, `"paths":null`, 1),
		strings.Replace(valid, `"tier":"tier1"`, `"tier":"tier3"`, 1),
		strings.Replace(valid, candidateTestCommit, "0123456", 1),
		strings.Replace(valid, "5000", "4999", 1),
		strings.Replace(valid, `"case":"allowed"`, `"case":"missing"`, 1),
		strings.Replace(valid, `"status":200`, `"status":200,"unexpected":true`, 1),
		strings.Repeat(" ", candidatePlanBytes+1),
	} {
		if _, err := readImpactCandidatePlan(strings.NewReader(input), candidateTestSHA([]byte(input))); err == nil {
			t.Fatal("accepted invalid candidate plan")
		}
	}
}

func TestImpactCandidateBuildCheck(t *testing.T) {
	var calls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/info", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("info read did not use the explicit bearer scope")
		}
		writeJSON(w, http.StatusOK, InfoResponse{Version: "1.7.0", Commit: candidateTestCommit, GraphStoreVersion: "v2"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	plan := impactCandidatePlan{Commit: candidateTestCommit, Version: "1.7.0"}
	if err := verifyImpactCandidateBuild(t.Context(), srv.URL, "fixture", plan); err != nil {
		t.Fatal(err)
	}
	plan.Commit = strings.Repeat("a", 40)
	if err := verifyImpactCandidateBuild(t.Context(), srv.URL, "fixture", plan); err == nil {
		t.Fatal("accepted build from a different commit")
	}
	for _, bad := range []string{"http://localhost:1234", srv.URL + "?token=x", srv.URL + "/path", "http://127.0.0.1"} {
		if err := verifyImpactCandidateBuild(t.Context(), bad, "fixture", plan); err == nil {
			t.Fatal("accepted unexpected endpoint")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("invalid endpoint reached server")
	}
}

func candidateFixtureHandler(t *testing.T) http.Handler {
	t.Helper()
	s := New("", seedImpactAPI(t), aggregator.NewRegistry(),
		WithClusterLister(&impactAPIClusters{clusters: []string{"prod", "hidden"}}),
		WithClusterRBAC(multicluster.NewRBACScope([]multicluster.RBACRule{{Token: "fixture", Clusters: []string{"prod"}}})),
		WithImpactEvidence(analysis.ImpactEvidenceProviders{
			Availability: impactAPIAvailability(func(_ context.Context, cluster string) (operations.AnalysisAvailability, error) {
				return operations.AnalysisAvailability{
					ClusterID: cluster, CapturedAt: time.Now(),
					Storage: operations.StorageAvailability{State: operations.StorageUnknown, DurabilityKnown: true},
					History: operations.HistoryAvailability{State: operations.HistoryDisabled, Coverage: operations.CoverageUnknown,
						Reasons: []operations.HistoryAvailabilityReason{operations.HistoryNotConfigured}, Content: "metadata_only", RetainedData: "not_queried"},
				}, nil
			}),
		}))
	return impactTestMux(s)
}

func TestImpactCandidateCasesAgainstRealHandler(t *testing.T) {
	srv := httptest.NewServer(candidateFixtureHandler(t))
	defer srv.Close()
	query := impact.Query{Namespace: "demo", Kind: "ConfigMap", Name: "settings", ClusterID: "prod", ExpectedUID: "root-instance",
		Relation: analysis.ImpactDependents, MaxDepth: 5, Limit: 200}
	allowed := impactCandidateCase{Query: query, Status: 200, Paths: map[string][]graph.EdgeType{"prod:other/Pod/consumer": {graph.EdgeTypeUsesConfigMap}}}
	allowed.History = &impactCandidateHistory{State: operations.HistoryDisabled, RetainedData: "not_queried", Reason: operations.HistoryNotConfigured}
	denied := impactCandidateCase{Query: query, Status: 403}
	denied.Query.ClusterID = "hidden"
	unauthenticated := impactCandidateCase{Query: query, Status: 401, Anonymous: true}
	conflict := impactCandidateCase{Query: query, Status: 409}
	conflict.Query.ExpectedUID = "replaced-instance"
	for _, c := range []impactCandidateCase{allowed, denied, unauthenticated, conflict} {
		if err := checkImpactCandidateCase(t.Context(), srv.URL, "fixture", version.Version, "tier1", c); err != nil {
			t.Fatalf("status %d: %v", c.Status, err)
		}
	}
	if err := checkImpactCandidateCase(t.Context(), srv.URL, "fixture", version.Version, "tier2", allowed); err == nil {
		t.Fatal("accepted memory evidence for a Tier 2 plan")
	}
	allowed.Paths["prod:other/Pod/consumer"] = []graph.EdgeType{graph.EdgeTypeOwns}
	if err := checkImpactCandidateCase(t.Context(), srv.URL, "fixture", version.Version, "tier1", allowed); err == nil {
		t.Fatal("accepted an incorrect relationship path")
	}
	denied.Query.ClusterID = "prod"
	if err := checkImpactCandidateCase(t.Context(), srv.URL, "fixture", version.Version, "tier1", denied); err == nil {
		t.Fatal("accepted successful disclosure in a denial case")
	}
}

func TestImpactCandidateCoverageRequiresDeclaredSource(t *testing.T) {
	row := operations.ResourceCoverage{Group: "example.com", Version: "v1", Resource: "widgets",
		State: operations.CoveragePartial, Reasons: []operations.CoverageReason{operations.CoverageWatchDenied}}
	snapshot := operations.CoverageSnapshot{Sources: []operations.CoverageSourceSnapshot{
		{Source: operations.CoverageSourceCRD, Resources: []operations.ResourceCoverage{row}},
	}}
	want := impactCandidateCoverage{Source: "gatekeeper", Group: row.Group, Version: row.Version,
		Resource: row.Resource, State: row.State, Reason: operations.CoverageWatchDenied}
	if want.matches(snapshot) {
		t.Fatal("accepted coverage from the wrong collector")
	}
	want.Source = "crd"
	if !want.matches(snapshot) {
		t.Fatal("missed denied watch on expected collector and endpoint")
	}
	want.Version = "v2"
	if want.matches(snapshot) {
		t.Fatal("accepted coverage from a different served version")
	}
}

func TestImpactCandidateSamplingRetainsOrderAndRejectsAnyFailure(t *testing.T) {
	calls := 0
	samples, err := sampleImpactCandidate(t.Context(), func(context.Context) error {
		calls++
		return nil
	})
	if err != nil || calls != 110 || len(samples) != 100 {
		t.Fatalf("calls=%d samples=%d err=%v", calls, len(samples), err)
	}
	for i := range samples {
		samples[i] = time.Duration(100-i) * time.Millisecond
	}
	before := slices.Clone(samples)
	if impactP95(samples) != 95*time.Millisecond || !slices.Equal(before, samples) {
		t.Fatal("p95 changed sample order or used the wrong rank")
	}
	for _, failAt := range []int{1, 10, 11, 110} {
		calls = 0
		failure := errors.New("failed sample")
		got, err := sampleImpactCandidate(t.Context(), func(context.Context) error {
			calls++
			if calls == failAt {
				return failure
			}
			return nil
		})
		if calls != failAt || got != nil || !errors.Is(err, failure) {
			t.Fatal("failed sample retried, ignored, or yielded a partial percentile")
		}
	}
}

// Make sure the plan can be generated from its fixture structs.
func TestImpactCandidateEmptyPathOracle(t *testing.T) {
	plan := impactCandidatePlan{Commit: candidateTestCommit, Version: "1.7.0", Tier: "tier1",
		Cases: []impactCandidateCase{{Name: "empty", Status: 200, Paths: map[string][]graph.EdgeType{},
			Query: impact.Query{Namespace: "demo", Kind: "ConfigMap", Name: "settings", Relation: analysis.ImpactDependents, MaxDepth: 1, Limit: 1}}}}
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readImpactCandidatePlan(strings.NewReader(string(b)), candidateTestSHA(b)); err != nil {
		t.Fatal(err)
	}
}

func TestImpactCandidateHistoryRejectsUnqualifiedBounds(t *testing.T) {
	marker := time.Now()
	expected := impactCandidateHistory{State: operations.HistoryDegraded, RetainedData: "observed",
		Reason: operations.HistoryWriteGap, LatestMarkerAt: &marker}
	evidence := &operations.HistoryRetentionEvidence{State: operations.RetentionObserved, Bounds: graph.HistoryBounds{LatestMarkerAt: marker}}
	history := operations.HistoryAvailability{State: operations.HistoryDegraded, RetainedData: "observed",
		Reasons: []operations.HistoryAvailabilityReason{operations.HistoryWriteGap}, RetentionEvidence: evidence}
	if !expected.matches(history) {
		t.Fatal("lost independent writer gap or fresh marker evidence")
	}
	for _, change := range []func(*operations.HistoryRetentionEvidence){
		func(e *operations.HistoryRetentionEvidence) { e.Stale = true },
		func(e *operations.HistoryRetentionEvidence) { e.Stopped = true },
		func(e *operations.HistoryRetentionEvidence) { e.Bounds.LatestMarkerAt = marker.Add(-time.Minute) },
	} {
		wrong := *evidence
		change(&wrong)
		history.RetentionEvidence = &wrong
		if expected.matches(history) {
			t.Fatal("accepted stale, stopped or incorrect marker evidence")
		}
	}
}

func TestImpactCandidatePinnedProbe(t *testing.T) {
	const fixtureVersion = "1.7.0-rc.1"
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, diagnostic      string
		wantInfo, wantQueries int64
	}{
		{name: "bound", wantInfo: 2, wantQueries: 111},
		{name: "edited", diagnostic: "candidate plan SHA-256 mismatch"},
		{name: "missing-digest", diagnostic: "candidate plan requires a lowercase SHA-256 digest"},
		{name: "closing-drift", diagnostic: "closing build check", wantInfo: 2, wantQueries: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := impactCandidatePlan{Commit: candidateTestCommit, Version: fixtureVersion, Tier: "tier1",
				Cases: []impactCandidateCase{{
					Name: "allowed", Status: http.StatusOK,
					Query: impact.Query{Namespace: "demo", Kind: "ConfigMap", Name: "settings", ClusterID: "prod",
						ExpectedUID: "root-instance", Relation: analysis.ImpactDependents, MaxDepth: 5, Limit: 200},
					Paths: map[string][]graph.EdgeType{"prod:other/Pod/consumer": {graph.EdgeTypeUsesConfigMap}},
				}},
			}
			if tc.name == "bound" {
				plan.Performance = &impactCandidatePerformance{Case: "allowed", Shape: "dense-5k", Resources: 5000, Edges: 18000, Host: "synthetic fixture"}
			}
			data, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			digest := candidateTestSHA(data)
			switch tc.name {
			case "edited":
				// The same JSON after reformatting has different sealed bytes.
				data = append(data, '\n')
			case "missing-digest":
				digest = ""
			}
			path := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}

			handler := candidateFixtureHandler(t)
			var infoReads, queryReads atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("probe changed HTTP method or bearer scope")
				}
				if r.URL.Path == "/api/v1/info" {
					read := infoReads.Add(1)
					commit := candidateTestCommit
					if read == 2 {
						if queryReads.Load() != tc.wantQueries {
							t.Error("closing build check preceded expected queries")
						}
						if tc.name == "closing-drift" {
							commit = strings.Repeat("a", 40)
						}
					}
					writeJSON(w, http.StatusOK, InfoResponse{Commit: commit, Version: fixtureVersion, GraphStoreVersion: "v2"})
					return
				}

				queryReads.Add(1)
				if infoReads.Load() != 1 {
					t.Error("query did not run between build checks")
				}
				// Serve the actual scoped handler against a seeded memory graph.
				// Stamp only the fixture version so no process globals are changed.
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, r)
				var capture ImpactResponse
				if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &capture) != nil {
					t.Error("fixture impact query failed")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				capture.KubeAtlasVersion = fixtureVersion
				writeJSON(w, http.StatusOK, capture)
			}))
			defer srv.Close()

			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestImpactFrozenCandidate$", "-test.v")
			cmd.Env = append(os.Environ(),
				"KUBEATLAS_IMPACT_CANDIDATE_PLAN="+path,
				"KUBEATLAS_IMPACT_CANDIDATE_PLAN_SHA256="+digest,
				"KUBEATLAS_IMPACT_CANDIDATE_SERVER="+srv.URL,
				"KUBEATLAS_TOKEN=fixture",
			)
			out, err := cmd.CombinedOutput()
			result := string(out)
			complete := "candidate probe complete plan-sha256=" + digest
			if tc.diagnostic == "" {
				if err != nil || !strings.Contains(result, complete) || !strings.Contains(result, "warmups=10 samples=100") {
					t.Fatalf("bound probe did not complete: %v\n%s", err, result)
				}
			} else if err == nil || !strings.Contains(result, tc.diagnostic) || strings.Contains(result, complete) {
				t.Fatalf("probe did not retain the expected failure: %v\n%s", err, result)
			}
			if infoReads.Load() != tc.wantInfo || queryReads.Load() != tc.wantQueries {
				t.Fatalf("unexpected probe sequence: info=%d queries=%d", infoReads.Load(), queryReads.Load())
			}
		})
	}
}

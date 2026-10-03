// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package impact

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func testQuery() Query {
	return Query{Namespace: "demo", Kind: "ConfigMap", Name: "settings", ExpectedUID: "root-uid", Relation: analysis.ImpactDependents, MaxDepth: 5, Limit: 200}
}

func testResponse(t *testing.T, q Query) *Response {
	t.Helper()
	store := memory.New()
	root := graph.Resource{Namespace: q.Namespace, Kind: q.Kind, Name: q.Name, ClusterID: q.ClusterID, UID: "root-uid", ResourceVersion: "10"}
	pod := graph.Resource{Namespace: "other", Kind: "Pod", Name: "api", ClusterID: q.ClusterID, UID: "pod-uid", ResourceVersion: "11"}
	for _, resource := range []graph.Resource{root, pod} {
		if err := store.UpsertResource(context.Background(), resource); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertEdge(context.Background(), graph.Edge{From: pod.ID(), To: root.ID(), Type: graph.EdgeTypeUsesConfigMap}); err != nil {
		t.Fatal(err)
	}
	a, err := analysis.AnalyzeImpactWithEvidence(context.Background(), store, q.rootID(), analysis.ImpactOptions{ClusterID: q.ClusterID,
		Relation: q.Relation, MaxDepth: q.MaxDepth, Limit: q.Limit, ExpectedUID: q.ExpectedUID}, graph.ImpactSnapshotOptions{ClusterID: q.ClusterID}, analysis.ImpactEvidenceProviders{})
	if err != nil {
		t.Fatal(err)
	}
	mode := "single"
	if q.ClusterID != "" {
		mode = "federated"
	}
	return &Response{SchemaVersion: "1", KubeAtlasVersion: "1.7.0-dev", GeneratedAt: time.Now().UTC(),
		Scope: Scope{ClusterID: q.ClusterID, Mode: mode, NamespaceScope: "all_namespaces_in_selected_cluster"}, Analysis: a, SharingWarning: SharingWarning}
}

func TestFetchExactQueryAndOneRequest(t *testing.T) {
	for _, cluster := range []string{"", "east"} {
		t.Run(cluster, func(t *testing.T) {
			q := testQuery()
			q.ClusterID = cluster
			response := testResponse(t, q)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/prefix/api/v1/impact/demo/ConfigMap/settings" || r.Header.Get("Authorization") != "Bearer synthetic-token" ||
					r.URL.Query().Get("cluster") != cluster || r.URL.Query().Get("expected_uid") != "root-uid" || r.URL.Query().Get("relation") != "dependents" ||
					r.URL.Query().Get("max_depth") != "5" || r.URL.Query().Get("limit") != "200" {
					t.Error("request contract mismatch")
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			got, err := Fetch(context.Background(), server.URL+"/prefix/", "synthetic-token", q)
			if err != nil || calls.Load() != 1 || got.Analysis.Counts.Total != 1 {
				t.Fatalf("fetch: err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestFetchClosedHTTPFailuresAndNoRedirect(t *testing.T) {
	for _, status := range []int{301, 302, 307, 308, 400, 401, 403, 404, 409, 413, 429, 500, 503, 504} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/stolen")
				w.WriteHeader(status)
				w.Write([]byte("private-response-canary synthetic-token"))
			}))
			defer server.Close()
			result, err := Fetch(context.Background(), server.URL, "synthetic-token", testQuery())
			if err == nil || result != nil || calls.Load() != 1 || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "synthetic-token") {
				t.Fatalf("unsafe failure: %v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestFetchRejectsMismatchedResponse(t *testing.T) {
	for name, change := range map[string]func(*Response){
		"schema":                 func(r *Response) { r.SchemaVersion = "2" },
		"cluster":                func(r *Response) { r.Scope.ClusterID = "west" },
		"mode":                   func(r *Response) { r.Scope.Mode = "federated" },
		"scope":                  func(r *Response) { r.Scope.NamespaceScope = "demo_only" },
		"root":                   func(r *Response) { r.Analysis.Root.ID = "demo/ConfigMap/other" },
		"uid":                    func(r *Response) { r.Analysis.Root.UID = "replaced" },
		"relation":               func(r *Response) { r.Analysis.Relation = analysis.ImpactDependencies },
		"depth":                  func(r *Response) { r.Analysis.MaxDepth = 1 },
		"limit":                  func(r *Response) { r.Analysis.Limit = 1 },
		"authorization limit":    func(r *Response) { r.Analysis.Authorization.Limit = 1 },
		"missing analysis":       func(r *Response) { r.Analysis = nil },
		"missing observation":    func(r *Response) { r.Analysis.Observation = nil },
		"missing availability":   func(r *Response) { r.Analysis.Availability = nil },
		"coverage cluster":       func(r *Response) { r.Analysis.Observation.Ordinary.After.ClusterID = "west" },
		"availability cluster":   func(r *Response) { r.Analysis.Availability.Evidence.ClusterID = "west" },
		"foreign result":         func(r *Response) { r.Analysis.Resources[0].Resource.ClusterID = "west" },
		"foreign path":           func(r *Response) { r.Analysis.Resources[0].Path[0].From = "west:other/Pod/api" },
		"inconsistent path":      func(r *Response) { r.Analysis.Resources[0].Path[0].TraversalTo = r.Analysis.Root.ID },
		"reference endpoint":     func(r *Response) { r.Analysis.ReferenceEvidence[0].To = "hidden/Secret/private" },
		"count":                  func(r *Response) { r.Analysis.Counts.Total = 99 },
		"direct count":           func(r *Response) { r.Analysis.Counts.Direct = 99 },
		"indirect count":         func(r *Response) { r.Analysis.Counts.Indirect = 99 },
		"authorization direct":   func(r *Response) { r.Analysis.Authorization.Counts.Direct = 99 },
		"authorization indirect": func(r *Response) { r.Analysis.Authorization.Counts.Indirect = 99 },
		"warning":                func(r *Response) { r.SharingWarning = "safe to delete" },
	} {
		t.Run(name, func(t *testing.T) {
			q := testQuery()
			r := testResponse(t, q)
			change(r)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(r)
			}))
			defer server.Close()
			if got, err := Fetch(context.Background(), server.URL, "", q); err == nil || got != nil {
				t.Fatal("accepted mismatched response")
			}
		})
	}
}

func TestFetchRejectsInvalidPayloads(t *testing.T) {
	raw, err := json.Marshal(testResponse(t, testQuery()))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"unknown payload": strings.Replace(string(raw), `"schemaVersion":`, `"raw":{"data":"private-canary"},"schemaVersion":`, 1),
		"trailing":        string(raw) + `{"private":"canary"}`,
		"oversize":        strings.Repeat(" ", MaxResponseBytes+1),
		"malformed":       `{"analysis":"private-canary"}`,
		"null":            "null",
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(body))
			}))
			defer server.Close()
			got, err := Fetch(context.Background(), server.URL, "", testQuery())
			if err == nil || got != nil || strings.Contains(err.Error(), "canary") {
				t.Fatalf("unsafe parse error: %v", err)
			}
		})
	}
}

func TestFetchCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Fetch(ctx, server.URL, "", testQuery()); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request never started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("cancellation error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not stop fetch")
	}
}

func TestQueryURLSafety(t *testing.T) {
	q := testQuery()
	for _, base := range []string{"http://example.com", "http://localhost", "https://user:password@example.com", "https://example.com?token=private", "https://example.com#secret", "file:///tmp/a", "https://example.com/../wrong", "https://example.com/%2e%2e/wrong", "https://%zz"} {
		if _, err := q.requestURL(base); err == nil || strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "private") {
			t.Errorf("accepted unsafe URL or exposed credentials")
		}
	}
	q.Namespace, q.Kind, q.Name, q.ClusterID = "", "ClusterRole", "system:reader+test", "east"
	u, err := q.requestURL("https://example.com/atlas/")
	if err != nil || !strings.Contains(u, "/atlas/api/v1/impact/_/ClusterRole/system:reader+test?") || !strings.Contains(u, "cluster=east") {
		t.Fatalf("URL=%s err=%v", u, err)
	}
	for _, change := range []func(*Query){
		func(q *Query) { q.MaxDepth = 0 }, func(q *Query) { q.MaxDepth = 11 }, func(q *Query) { q.Limit = 1001 },
		func(q *Query) { q.Relation = "both" }, func(q *Query) { q.Name = "../secret" }, func(q *Query) { q.Name = ".." },
		func(q *Query) { q.Namespace = "_" }, func(q *Query) { q.ClusterID = "east,west" }, func(q *Query) { q.ExpectedUID = "bad\nuid" },
	} {
		bad := testQuery()
		change(&bad)
		if bad.Validate() == nil {
			t.Error("accepted invalid query")
		}
	}
}

func TestFetchRejectsNestedCoverageScope(t *testing.T) {
	for _, cluster := range []string{"", "east"} {
		for _, facet := range []int{0, 1, 2, 3} {
			for _, inventory := range []bool{false, true} {
				t.Run(fmt.Sprintf("cluster=%s/facet=%d/inventory=%t", cluster, facet, inventory), func(t *testing.T) {
					q := testQuery()
					q.ClusterID = cluster
					r := testResponse(t, q)
					snapshots := []*operations.CoverageSnapshot{
						&r.Analysis.Observation.Ordinary.Before, &r.Analysis.Observation.Ordinary.After,
						&r.Analysis.Observation.Authorization.Before, &r.Analysis.Observation.Authorization.After,
					}
					if inventory {
						snapshots[facet].APIInventory = &operations.APIInventoryEvidence{ClusterID: "west"}
					} else {
						snapshots[facet].Sources = []operations.CoverageSourceSnapshot{{ClusterID: "west", Source: operations.CoverageSourceCRD}}
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(r)
					}))
					defer server.Close()
					if got, err := Fetch(context.Background(), server.URL, "", q); err == nil || got != nil {
						t.Fatal("accepted foreign nested coverage")
					}
					if _, err := Render(r, "html"); err == nil {
						t.Fatal("exported foreign nested coverage")
					}
					if inventory {
						snapshots[facet].APIInventory.ClusterID = cluster
					} else {
						snapshots[facet].Sources[0].ClusterID = cluster
					}
					if err := validateResponse(r, q); err != nil {
						t.Fatalf("rejected matching nested scope: %v", err)
					}
					if _, err := Render(r, "html"); err != nil {
						t.Fatalf("matching export failed: %v", err)
					}

				})
			}
		}
	}
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/multicluster"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type impactAPIStore struct {
	graph.GraphStore
	calls atomic.Int64
	read  func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error)
}

func (s *impactAPIStore) SnapshotImpact(ctx context.Context, o graph.ImpactSnapshotOptions) (*graph.Graph, error) {
	s.calls.Add(1)
	return s.read(ctx, o)
}

type impactAPIClusters struct {
	clusters []string
	calls    atomic.Int64
}

func (c *impactAPIClusters) ListClusters() []string { c.calls.Add(1); return c.clusters }

type impactAPICoverage func(context.Context, string, []schema.GroupVersionResource) (operations.CoverageSnapshot, error)

func (f impactAPICoverage) SnapshotCoverage(c context.Context, s string, g []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
	return f(c, s, g)
}

type impactAPIAvailability func(context.Context, string) (operations.AnalysisAvailability, error)

func (f impactAPIAvailability) SnapshotAvailability(c context.Context, s string) (operations.AnalysisAvailability, error) {
	return f(c, s)
}

func impactTestMux(s *Server) http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return mux
}
func impactTestRequest(ctx context.Context, path, token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}
func serveImpactTest(s *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	impactTestMux(s).ServeHTTP(w, r)
	return w
}
func seedImpactAPI(t *testing.T) *memory.Store {
	t.Helper()
	s := memory.New()
	for _, cluster := range []string{"", "prod", "hidden"} {
		root := graph.Resource{ClusterID: cluster, Namespace: "demo", Kind: "ConfigMap", Name: "settings", UID: "root-instance", ResourceVersion: "10", GroupVersion: "v1", Raw: map[string]any{"data": "synthetic-payload-canary"}}
		pod := graph.Resource{ClusterID: cluster, Namespace: "other", Kind: "Pod", Name: "consumer", UID: "pod-instance", ResourceVersion: "11", GroupVersion: "v1"}
		if err := s.UpsertResource(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertResource(context.Background(), pod); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertEdge(context.Background(), graph.Edge{From: pod.ID(), To: root.ID(), Type: graph.EdgeTypeUsesConfigMap}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestImpactAPIAuthorizationBeforeEveryRead(t *testing.T) {
	for _, tc := range []struct {
		name, query, token string
		status             int
		lists              int64
	}{
		{"no token", "prod", "", 401, 0}, {"unknown token", "prod", "unknown", 403, 0},
		{"denied attached", "hidden", "prod-token", 403, 0}, {"denied absent", "absent", "prod-token", 403, 0},
		{"allowed absent", "gone", "gone-token", 404, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clusters := &impactAPIClusters{clusters: []string{"prod", "hidden"}}
			spy := &impactAPIStore{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) {
				t.Error("unauthorized graph read")
				return nil, errors.New("private canary")
			}}
			var evidenceReads atomic.Int64
			s := New("", spy, aggregator.NewRegistry(), WithClusterLister(clusters),
				WithClusterRBAC(multicluster.NewRBACScope([]multicluster.RBACRule{{Token: "prod-token", Clusters: []string{"prod"}}, {Token: "gone-token", Clusters: []string{"gone"}}})),
				WithImpactEvidence(analysis.ImpactEvidenceProviders{
					Coverage: impactAPICoverage(func(context.Context, string, []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
						evidenceReads.Add(1)
						return operations.CoverageSnapshot{}, nil
					}),
					Availability: impactAPIAvailability(func(context.Context, string) (operations.AnalysisAvailability, error) {
						evidenceReads.Add(1)
						return operations.AnalysisAvailability{}, nil
					}),
				}))
			w := serveImpactTest(s, impactTestRequest(context.Background(), "/api/v1/impact/demo/ConfigMap/settings?cluster="+tc.query, tc.token))
			if w.Code != tc.status || spy.calls.Load() != 0 || evidenceReads.Load() != 0 || clusters.calls.Load() != tc.lists {
				t.Fatalf("status=%d graph=%d evidence=%d lists=%d", w.Code, spy.calls.Load(), evidenceReads.Load(), clusters.calls.Load())
			}
			for _, private := range []string{"hidden", "absent", "gone", "settings", "canary", "root-instance", "analysis\""} {
				if strings.Contains(w.Body.String(), private) {
					t.Fatalf("denial leaked %q", private)
				}
			}
		})
	}
}

func TestImpactAPIScopeEvidenceAndWireContract(t *testing.T) {
	for _, cluster := range []string{"", "prod"} {
		t.Run("cluster="+cluster, func(t *testing.T) {
			store := seedImpactAPI(t)
			var mu sync.Mutex
			var reads []string
			record := func(s string) { mu.Lock(); defer mu.Unlock(); reads = append(reads, s) }
			spy := &impactAPIStore{GraphStore: store, read: func(ctx context.Context, o graph.ImpactSnapshotOptions) (*graph.Graph, error) {
				record("graph")
				if o.ClusterID != cluster {
					t.Error("substituted cluster")
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > impactRequestTimeout {
					t.Error("missing overall deadline")
				}
				return store.SnapshotImpact(ctx, o)
			}}
			tracker := operations.NewCoverageTracker(time.Minute)
			availability := snapshot.NewAnalysisAvailabilityReader(nil, nil, snapshot.AnalysisAvailabilityConfig{Federated: cluster != ""})
			opts := []ServerOption{WithImpactEvidence(analysis.ImpactEvidenceProviders{
				Coverage: impactAPICoverage(func(ctx context.Context, c string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
					record("coverage")
					return tracker.SnapshotCoverage(ctx, c, types)
				}),
				Availability: impactAPIAvailability(func(ctx context.Context, c string) (operations.AnalysisAvailability, error) {
					record("availability")
					return availability.SnapshotAvailability(ctx, c)
				}),
			})}
			query, token := "?expected_uid=root-instance", ""
			if cluster != "" {
				opts = append(opts, WithClusterLister(&impactAPIClusters{clusters: []string{"prod", "hidden"}}), WithClusterRBAC(multicluster.NewRBACScope([]multicluster.RBACRule{{Token: "prod-token", Clusters: []string{"prod"}}})))
				query += "&cluster=prod"
				token = "prod-token"
			}
			s := New("", spy, aggregator.NewRegistry(), opts...)
			w := serveImpactTest(s, impactTestRequest(context.Background(), "/api/v1/impact/demo/ConfigMap/settings"+query, token))
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var body ImpactResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.SchemaVersion != "1" || body.KubeAtlasVersion == "" || body.GeneratedAt.IsZero() || body.Scope.ClusterID != cluster || body.Scope.NamespaceScope != "all_namespaces_in_selected_cluster" || body.SharingWarning == "" {
				t.Fatal("missing envelope identity or scope")
			}
			a := body.Analysis
			if a == nil || a.Counts.Total != 1 || a.Resources[0].Resource.Namespace != "other" || a.Resources[0].Resource.ClusterID != cluster || a.ReferenceEvidence[0].Status != "unknown" {
				t.Fatal("wrong selected-cluster or cross-namespace result")
			}
			if a.Observation == nil || a.Observation.Ordinary.EmptyResultAssessment == "none_found_in_analyzed_scope" || a.Availability == nil || a.Availability.Evidence.History.RetainedData != "not_queried" || a.Availability.Evidence.History.State != operations.HistoryDisabled {
				t.Fatal("missing/inflated evidence")
			}
			for _, forbidden := range []string{"synthetic-payload-canary", "hidden:", "\"raw\"", "Bearer", "prod-token"} {
				if strings.Contains(w.Body.String(), forbidden) {
					t.Fatalf("response leaked %q", forbidden)
				}
			}
			mu.Lock()
			gotReads := append([]string(nil), reads...)
			mu.Unlock()
			if !reflect.DeepEqual(gotReads, []string{"coverage", "graph", "coverage", "availability"}) {
				t.Fatalf("read order=%v", gotReads)
			}
			if spy.calls.Load() != 1 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Vary") != "Authorization" || len(w.Body.Bytes()) > impactResponseBytes {
				t.Fatal("request ownership/cache/size boundary failed")
			}
		})
	}
}

func TestImpactAPIInvalidInputNeverReads(t *testing.T) {
	for _, query := range []string{"?cluster=prod", "?cluster=", "?limit=0", "?limit=-1", "?limit=1001", "?limit=01", "?limit=1&limit=2", "?max_depth=0", "?max_depth=11", "?max_depth=banana", "?relation=upstream", "?relation=", "?expected_uid=", "?namespace=other", "?label_selector=app=x", "?x=%zz", "?limit=1;cluster=hidden", "?expected_uid=%00"} {
		t.Run(query, func(t *testing.T) {
			spy := &impactAPIStore{}
			s := New("", spy, aggregator.NewRegistry())
			w := serveImpactTest(s, impactTestRequest(context.Background(), "/api/v1/impact/demo/ConfigMap/settings"+query, ""))
			if w.Code != 400 || spy.calls.Load() != 0 {
				t.Fatalf("status=%d reads=%d", w.Code, spy.calls.Load())
			}
		})
	}
	for _, query := range []string{"", "?cluster=", "?cluster=prod,hidden", "?cluster=prod&cluster=prod", "?cluster=prod%3Aother", "?cluster=prod%2Fother"} {
		spy := &impactAPIStore{}
		s := New("", spy, aggregator.NewRegistry(), WithClusterLister(&impactAPIClusters{}))
		w := serveImpactTest(s, impactTestRequest(context.Background(), "/api/v1/impact/demo/ConfigMap/settings"+query, ""))
		if w.Code != 400 || spy.calls.Load() != 0 {
			t.Fatalf("federation query %q status=%d", query, w.Code)
		}
	}
}

func TestImpactAPIUIDSecretIdentityAndTruncation(t *testing.T) {
	store := seedImpactAPI(t)
	s := New("", store, aggregator.NewRegistry())
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/v1/impact/demo/ConfigMap/settings?expected_uid=old", 409},
		{"/api/v1/impact/demo/ConfigMap/missing", 404},
		{"/api/v1/impact/other/Pod/consumer?relation=dependencies", 200},
		{"/api/v1alpha1/impact/demo/ConfigMap/settings", 404},
	} {
		w := serveImpactTest(s, impactTestRequest(context.Background(), tc.path, ""))
		if w.Code != tc.status {
			t.Fatalf("%s status=%d body=%s", tc.path, w.Code, w.Body.String())
		}
	}
	ctx := context.Background()
	secret := graph.SecretReferenceResource("demo", "ref", "")
	if err := store.UpsertResource(ctx, secret); err != nil {
		t.Fatal(err)
	}
	w := serveImpactTest(s, impactTestRequest(ctx, "/api/v1/impact/demo/Secret/ref", ""))
	var body ImpactResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !body.Analysis.Root.ReferenceOnly || body.Analysis.Root.UID != "" || body.Analysis.Root.ResourceVersion != "" {
		t.Fatal("Secret ceased to be reference-only")
	}
	w = serveImpactTest(s, impactTestRequest(ctx, "/api/v1/impact/demo/Secret/ref?expected_uid=known", ""))
	if w.Code != 409 {
		t.Fatal("Secret UID must remain unverifiable")
	}
	role := graph.Resource{Kind: "ClusterRole", Name: "system:node", UID: "role-instance"}
	if err := store.UpsertResource(ctx, role); err != nil {
		t.Fatal(err)
	}
	w = serveImpactTest(s, impactTestRequest(ctx, "/api/v1/impact/_/ClusterRole/system:node", ""))
	if w.Code != 200 {
		t.Fatalf("cluster-scoped system role: %d %s", w.Code, w.Body.String())
	}
	second := graph.Resource{Namespace: "demo", Kind: "Pod", Name: "second"}
	if err := store.UpsertResource(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertEdge(ctx, graph.Edge{From: second.ID(), To: "demo/ConfigMap/settings", Type: graph.EdgeTypeUsesConfigMap}); err != nil {
		t.Fatal(err)
	}
	w = serveImpactTest(s, impactTestRequest(ctx, "/api/v1/impact/demo/ConfigMap/settings?limit=1", ""))
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !body.Analysis.Truncated || !body.Analysis.Counts.LowerBound || body.Analysis.Counts.Total != 1 {
		t.Fatal("limit truncation was not explicit")
	}
}

func TestImpactAPIErrorsNeverExposeBackendDetails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"store error", errors.New("synthetic-db-password-canary"), 500},
		{"read limit", graph.ErrImpactSnapshotLimit, 413},
		{"projection", analysis.ErrImpactInvalidProjection, 500},
		{"panic", nil, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &impactAPIStore{read: func(context.Context, graph.ImpactSnapshotOptions) (*graph.Graph, error) {
				if tc.name == "panic" {
					panic("synthetic-db-password-canary")
				}
				return nil, tc.err
			}}
			w := serveImpactTest(New("", spy, aggregator.NewRegistry()), impactTestRequest(context.Background(), "/api/v1/impact/demo/Pod/root", ""))
			if w.Code != tc.status || strings.Contains(w.Body.String(), "canary") || strings.Contains(w.Body.String(), "root") {
				t.Fatalf("unsafe error %d %s", w.Code, w.Body.String())
			}
		})
	}
	// Embedding the legacy store does not promote optional capabilities.
	wrapped := struct{ graph.GraphStore }{seedImpactAPI(t)}
	w := serveImpactTest(New("", wrapped, aggregator.NewRegistry()), impactTestRequest(context.Background(), "/api/v1/impact/demo/ConfigMap/settings", ""))
	if w.Code != 503 {
		t.Fatal("restricted legacy wrapper was unwrapped or given a fallback")
	}
}

func TestImpactAPIDeadlineCancellationAndAdmission(t *testing.T) {
	started := make(chan context.Context, impactConcurrency)
	release := make(chan struct{})
	var closeOnce sync.Once
	defer closeOnce.Do(func() { close(release) })
	spy := &impactAPIStore{read: func(ctx context.Context, _ graph.ImpactSnapshotOptions) (*graph.Graph, error) {
		started <- ctx
		<-release
		return &graph.Graph{}, nil
	}}
	s := New("", spy, aggregator.NewRegistry())
	for range impactConcurrency {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		w := serveImpactTest(s, impactTestRequest(ctx, "/api/v1/impact/demo/Pod/root", ""))
		cancel()
		if w.Code != 504 || strings.Contains(w.Body.String(), "\"analysis\"") {
			t.Fatalf("deadline response=%d %s", w.Code, w.Body.String())
		}
		select {
		case worker := <-started:
			if worker.Err() == nil {
				t.Fatal("worker did not receive cancellation")
			}
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	w := serveImpactTest(s, impactTestRequest(context.Background(), "/api/v1/impact/demo/Pod/root", ""))
	if w.Code != 429 || spy.calls.Load() != impactConcurrency {
		t.Fatal("timed-out uncooperative workers released admission prematurely")
	}
	closeOnce.Do(func() { close(release) })
	deadline := time.Now().Add(time.Second)
	for len(s.impactSem) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.impactSem) != 0 {
		t.Fatal("completed workers retained admission")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	w = serveImpactTest(s, impactTestRequest(cancelled, "/api/v1/impact/demo/Pod/root", ""))
	if w.Body.Len() != 0 || spy.calls.Load() != impactConcurrency {
		t.Fatal("cancelled caller received analysis or started work")
	}
}

func TestImpactAPICompletedResponsesReleaseAdmissionSynchronously(t *testing.T) {
	s := New("", seedImpactAPI(t), aggregator.NewRegistry())
	for i := range 100 {
		w := serveImpactTest(s, impactTestRequest(context.Background(), "/api/v1/impact/demo/ConfigMap/settings", ""))
		if w.Code != 200 || len(s.impactSem) != 0 {
			t.Fatalf("completed request %d: status=%d occupied=%d", i, w.Code, len(s.impactSem))
		}
	}
}

func TestImpactAPIResponseCapFailsBeforeSuccess(t *testing.T) {
	store := memory.New()
	root := graph.Resource{Namespace: "demo", Kind: "Pod", Name: "large", UID: types.UID(strings.Repeat("u", 1_100_000)), ResourceVersion: strings.Repeat("v", 1_100_000)}
	if err := store.UpsertResource(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	w := serveImpactTest(New("", store, aggregator.NewRegistry()), impactTestRequest(context.Background(), "/api/v1/impact/demo/Pod/large", ""))
	if w.Code != 413 || w.Body.Len() > 1024 || strings.Contains(w.Body.String(), "\"analysis\"") {
		t.Fatalf("oversized response %d length=%d", w.Code, w.Body.Len())
	}
}

func TestImpactAPIDynamicSourceEvidenceIsScopeBound(t *testing.T) {
	tracker := operations.NewCoverageTracker(time.Minute)
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	for _, cluster := range []string{"prod", "hidden"} {
		inventory, err := tracker.BeginAPIInventory(cluster)
		if err != nil {
			t.Fatal(err)
		}
		kind := "Widget"
		if cluster == "hidden" {
			kind = "HiddenWidget"
		}
		if err := inventory.Record(operations.APIInventoryComplete, []operations.APIResourceDescriptor{{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Kind: kind, Namespaced: true, List: true, Watch: true}}, false, time.Now()); err != nil {
			t.Fatal(err)
		}
		gatekeeper, err := tracker.BeginSource(cluster, operations.CoverageSourceGatekeeper, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		state := operations.SourceAPINotAdvertised
		if cluster == "hidden" {
			state = operations.SourceAPIDenied
		}
		if err := gatekeeper.RecordDiscovery(state, time.Now()); err != nil {
			t.Fatal(err)
		}
		source, err := tracker.BeginSource(cluster, operations.CoverageSourceCRD, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		token, err := source.BeginType(gvr, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		token.InitialDeliveryComplete(gvr)
		token.WatchStarted(gvr)
		if cluster == "hidden" {
			token.RecordGap(gvr, operations.CoveragePersistenceFailed, time.Now())
		}
	}
	s := New("", seedImpactAPI(t), aggregator.NewRegistry(), WithClusterLister(&impactAPIClusters{clusters: []string{"prod", "hidden"}}),
		WithClusterRBAC(multicluster.NewRBACScope([]multicluster.RBACRule{{Token: "prod-token", Clusters: []string{"prod"}}})),
		WithImpactEvidence(analysis.ImpactEvidenceProviders{Coverage: tracker}))
	w := serveImpactTest(s, impactTestRequest(context.Background(), "/api/v1/impact/demo/ConfigMap/settings?cluster=prod", "prod-token"))
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var body ImpactResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ordinary := body.Analysis.Observation.Ordinary
	if ordinary.Before.APIInventory == nil || ordinary.Before.APIInventory.ClusterID != "prod" || ordinary.Before.APIInventory.Resources[0].Kind != "Widget" || len(ordinary.OptionalAPIs) != 7 {
		t.Fatal("inventory missing from authorized response")
	}
	if len(ordinary.Before.Sources) != 2 || ordinary.Before.Sources[0].ClusterID != "prod" || ordinary.Before.Sources[0].State != operations.CoverageObserved || ordinary.TypeScopeClosed {
		t.Fatal("source identity or qualification lost in HTTP envelope")
	}
	if d := ordinary.Before.Sources[1].Discovery; d == nil || d.State != operations.SourceAPINotAdvertised || d.CheckedAt.IsZero() || d.Revision != 1 {
		t.Fatal("Gatekeeper discovery evidence missing from authorized HTTP response")
	}
	if strings.Contains(w.Body.String(), "hidden") || strings.Contains(w.Body.String(), "HiddenWidget") || strings.Contains(w.Body.String(), "persistence_gap") || strings.Contains(w.Body.String(), "permission_denied") {
		t.Fatal("unselected source evidence leaked")
	}
}

func TestImpactAPIEvidenceFailureNeverReturnsPartialAnalysis(t *testing.T) {
	for _, stage := range []string{"coverage before", "coverage after", "availability"} {
		t.Run(stage, func(t *testing.T) {
			store := seedImpactAPI(t)
			spy := &impactAPIStore{GraphStore: store, read: store.SnapshotImpact}
			tracker := operations.NewCoverageTracker(time.Minute)
			var coverageCalls atomic.Int64
			providers := analysis.ImpactEvidenceProviders{
				Coverage: impactAPICoverage(func(ctx context.Context, cluster string, types []schema.GroupVersionResource) (operations.CoverageSnapshot, error) {
					n := coverageCalls.Add(1)
					if stage == "coverage before" && n == 1 || stage == "coverage after" && n == 2 {
						return operations.CoverageSnapshot{}, errors.New("synthetic-provider-canary")
					}
					return tracker.SnapshotCoverage(ctx, cluster, types)
				}),
				Availability: impactAPIAvailability(func(context.Context, string) (operations.AnalysisAvailability, error) {
					return operations.AnalysisAvailability{}, errors.New("synthetic-provider-canary")
				}),
			}
			s := New("", spy, aggregator.NewRegistry(), WithImpactEvidence(providers))
			w := serveImpactTest(s, impactTestRequest(context.Background(), "/api/v1/impact/demo/ConfigMap/settings", ""))
			if w.Code != 503 {
				t.Fatalf("status=%d", w.Code)
			}
			for _, text := range []string{"canary", "settings", "consumer", "\"counts\"", "\"analysis\""} {
				if strings.Contains(w.Body.String(), text) {
					t.Fatal("failed evidence exposed a partial result")
				}
			}
			wantReads := int64(1)
			if stage == "coverage before" {
				wantReads = 0
			}
			if spy.calls.Load() != wantReads {
				t.Fatalf("graph reads=%d want=%d", spy.calls.Load(), wantReads)
			}
		})
	}
}

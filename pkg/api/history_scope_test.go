// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

type historyScopeStore struct {
	graph.GraphStore
	events         func(context.Context, graph.HistoryEventQuery) (graph.HistoryEventPage, error)
	markers        func(context.Context, string) ([]graph.SnapshotMeta, error)
	scoped, legacy atomic.Int64
}

func (s *historyScopeStore) ReadHistoryEvents(ctx context.Context, q graph.HistoryEventQuery) (graph.HistoryEventPage, error) {
	s.scoped.Add(1)
	return s.events(ctx, q)
}
func (s *historyScopeStore) ReadHistoryMarkers(ctx context.Context, c string) ([]graph.SnapshotMeta, error) {
	s.scoped.Add(1)
	return s.markers(ctx, c)
}
func (s *historyScopeStore) ListEvents(context.Context, string, time.Time, time.Time) ([]graph.ResourceEvent, error) {
	s.legacy.Add(1)
	return nil, errors.New("private legacy history canary")
}
func (s *historyScopeStore) ListSnapshotMeta(context.Context) ([]graph.SnapshotMeta, error) {
	s.legacy.Add(1)
	return nil, errors.New("private legacy marker canary")
}

func serveHistory(s *Server, path, token string) *httptest.ResponseRecorder {
	return serveImpactTest(s, impactTestRequest(context.Background(), path, token))
}
func historyRBAC() *multicluster.RBACScope {
	return multicluster.NewRBACScope([]multicluster.RBACRule{{Token: "prod-token", Clusters: []string{"prod"}}, {Token: "gone-token", Clusters: []string{"gone"}}})
}

func TestHistoryScopeAuthorizationPrecedesAllReads(t *testing.T) {
	for _, suffix := range []string{"snapshots?", "snapshots/diff?from=1h&"} {
		for _, tc := range []struct {
			name, query, token string
			status             int
			lists              int64
		}{
			{"missing token", "cluster=prod", "", 401, 0}, {"unknown token", "cluster=prod", "bad", 403, 0},
			{"denied attached", "cluster=hidden", "prod-token", 403, 0}, {"denied absent", "cluster=absent", "prod-token", 403, 0},
			{"authorized absent", "cluster=gone", "gone-token", 404, 1}, {"missing selection", "", "prod-token", 400, 0},
			{"repeated selection", "cluster=prod&cluster=prod", "prod-token", 400, 0}, {"multiple selection", "cluster=prod,hidden", "prod-token", 400, 0},
			{"empty selection", "cluster=", "prod-token", 400, 0}, {"whitespace", "cluster=%20prod", "prod-token", 400, 0},
			{"invalid separator", "cluster=prod:other", "prod-token", 400, 0},
		} {
			t.Run(suffix+tc.name, func(t *testing.T) {
				store := &historyScopeStore{}
				clusters := &impactAPIClusters{clusters: []string{"prod", "hidden"}}
				s := New("", store, aggregator.NewRegistry(), WithSnapshots(time.Hour), WithClusterLister(clusters), WithClusterRBAC(historyRBAC()))
				w := serveHistory(s, "/api/v1/"+suffix+tc.query, tc.token)
				if w.Code != tc.status || store.scoped.Load() != 0 || store.legacy.Load() != 0 || clusters.calls.Load() != tc.lists {
					t.Fatalf("code=%d reads=%d legacy=%d lists=%d body=%s", w.Code, store.scoped.Load(), store.legacy.Load(), clusters.calls.Load(), w.Body)
				}
				if strings.Contains(w.Body.String(), "canary") || strings.Contains(w.Body.String(), "hidden") {
					t.Fatal("closed error disclosed private data")
				}
				if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Vary") != "Authorization" {
					t.Fatal("authorized history response lacks cache isolation")
				}
				if tc.status == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Fatal("missing authentication challenge")
				}

			})
		}
	}
}

func TestHistoryScopeActualRoutesIsolateSameUIDAndUnlabelledData(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	base := time.Now().Add(-30 * time.Minute)
	for _, c := range []string{"", "prod", "hidden"} {
		for i, et := range []graph.EventType{graph.EventTypeAdd, graph.EventTypeUpdate, graph.EventTypeDelete} {
			e := graph.ResourceEvent{ClusterID: c, Timestamp: base.Add(time.Duration(i) * time.Minute), Namespace: "demo", Kind: "ConfigMap", Name: c + "-name", UID: "same-uid", EventType: et, Data: map[string]any{"private": "payload-canary"}}
			if c == "prod" {
				e.EventType = graph.EventTypeUpdate
			}
			if err := store.AppendEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.AppendSnapshotMeta(ctx, graph.SnapshotMeta{ClusterID: c, Timestamp: base, ResourceCount: 7, Trigger: graph.SnapshotTriggerManual}); err != nil {
			t.Fatal(err)
		}
	}
	// Hidden/Secret rows must neither change the selected count nor consume a
	// reader budget. Secret-object events never become diff entries.
	if err := store.AppendEvent(ctx, graph.ResourceEvent{ClusterID: "prod", Timestamp: base, Kind: "Secret", Name: "secret-canary", EventType: graph.EventTypeAdd}); err != nil {
		t.Fatal(err)
	}
	spy := &historyScopeStore{GraphStore: store, events: store.ReadHistoryEvents, markers: store.ReadHistoryMarkers}
	s := New("", spy, aggregator.NewRegistry(), WithSnapshots(time.Hour), WithClusterLister(&impactAPIClusters{clusters: []string{"prod", "hidden"}}), WithClusterRBAC(historyRBAC()))
	w := serveHistory(s, "/api/v1/snapshots?cluster=prod", "prod-token")
	var markers SnapshotListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &markers); err != nil || w.Code != 200 || markers.Count != 1 || markers.Snapshots[0].ClusterID != "prod" {
		t.Fatalf("markers: %s", w.Body)
	}
	w = serveHistory(s, "/api/v1/snapshots/diff?from=1h&cluster=prod&namespace=demo", "prod-token")
	var diff analysis.DiffResult
	if err := json.Unmarshal(w.Body.Bytes(), &diff); err != nil || w.Code != 200 || len(diff.Added) != 0 || len(diff.Removed) != 0 || len(diff.Modified) != 1 || diff.Modified[0].Name != "prod-name" {
		t.Fatalf("selected diff mixed same UID: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "canary") || strings.Contains(w.Body.String(), "hidden-name") || spy.legacy.Load() != 0 {
		t.Fatal("scoped route leaked data or used global fallback")
	}
	// Unknown provenance of unlabelled rows is preserved in standalone mode.
	standalone := New("", store, aggregator.NewRegistry(), WithSnapshots(time.Hour))
	for _, version := range []string{"v1", "v1alpha1"} {
		w = serveHistory(standalone, "/api/"+version+"/snapshots", "")
		markers = SnapshotListResponse{}
		if err := json.Unmarshal(w.Body.Bytes(), &markers); err != nil || w.Code != 200 || markers.Count != 1 || markers.Snapshots[0].ClusterID != "" {
			t.Fatal("standalone selected named rows")
		}
	}
}

func TestHistoryAlphaAliasesCannotBypassMemberSelection(t *testing.T) {
	for _, withRBAC := range []bool{false, true} {
		spy := &historyScopeStore{}
		opts := []ServerOption{WithSnapshots(time.Hour), WithClusterLister(&impactAPIClusters{clusters: []string{"prod"}})}
		if withRBAC {
			opts = append(opts, WithClusterRBAC(historyRBAC()))
		}
		s := New("", spy, aggregator.NewRegistry(), opts...)
		for _, path := range []string{"/api/v1alpha1/snapshots", "/api/v1alpha1/snapshots/diff?from=1h", "/api/v1alpha1/snapshots?cluster=prod"} {
			if w := serveHistory(s, path, "prod-token"); w.Code != 503 || spy.scoped.Load() != 0 || spy.legacy.Load() != 0 {
				t.Fatal("alpha alias bypassed single-member history scope")
			}
		}
	}
}

func TestHistoryScopeNoImplicitCapabilityFallback(t *testing.T) {
	// GraphStore embedding deliberately hides both optional history capabilities.
	store := struct{ graph.GraphStore }{memory.New()}
	s := New("", store, aggregator.NewRegistry(), WithSnapshots(time.Hour))
	for _, path := range []string{"/api/v1/snapshots", "/api/v1/snapshots/diff?from=1h"} {
		if w := serveHistory(s, path, ""); w.Code != 503 {
			t.Fatal("optional capability fell back to unscoped legacy read")
		}
	}
}

func TestHistoryScopeRejectsTruncationForeignMetadataAndProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		events  func(context.Context, graph.HistoryEventQuery) (graph.HistoryEventPage, error)
		markers func(context.Context, string) ([]graph.SnapshotMeta, error)
		status  int
	}{
		{name: "truncated", events: func(context.Context, graph.HistoryEventQuery) (graph.HistoryEventPage, error) {
			return graph.HistoryEventPage{Truncated: true}, nil
		}, status: 413},
		{name: "foreign event", events: func(_ context.Context, q graph.HistoryEventQuery) (graph.HistoryEventPage, error) {
			return graph.HistoryEventPage{Events: []graph.HistoryEvent{{ClusterID: "hidden", Timestamp: q.From}}}, nil
		}, status: 500},
		{name: "Secret event", events: func(_ context.Context, q graph.HistoryEventQuery) (graph.HistoryEventPage, error) {
			return graph.HistoryEventPage{Events: []graph.HistoryEvent{{ClusterID: q.ClusterID, Kind: "Secret", Timestamp: q.From}}}, nil
		}, status: 500},
		{name: "unordered events", events: func(_ context.Context, q graph.HistoryEventQuery) (graph.HistoryEventPage, error) {
			return graph.HistoryEventPage{Events: []graph.HistoryEvent{{ID: 2, Timestamp: q.From.Add(time.Second)}, {ID: 1, Timestamp: q.From}}}, nil
		}, status: 500},
		{name: "foreign marker", markers: func(context.Context, string) ([]graph.SnapshotMeta, error) {
			return []graph.SnapshotMeta{{ClusterID: "hidden", ResourceCount: 12345}}, nil
		}, status: 500},
		{name: "oversized marker", markers: func(context.Context, string) ([]graph.SnapshotMeta, error) {
			return []graph.SnapshotMeta{{Trigger: graph.SnapshotTrigger(strings.Repeat("large", graph.MaxHistoryBytes/5))}}, nil
		}, status: 413},
		{name: "database error", events: func(context.Context, graph.HistoryEventQuery) (graph.HistoryEventPage, error) {
			return graph.HistoryEventPage{}, errors.New("private-provider-canary")
		}, status: 500},
		{name: "panic", events: func(context.Context, graph.HistoryEventQuery) (graph.HistoryEventPage, error) {
			panic("private-panic-canary")
		}, status: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &historyScopeStore{events: tc.events, markers: tc.markers}
			path := "/api/v1/snapshots/diff?from=1h"
			if tc.markers != nil {
				path = "/api/v1/snapshots"
			}
			s := New("", store, aggregator.NewRegistry(), WithSnapshots(time.Hour))
			w := serveHistory(s, path, "")
			if w.Code != tc.status || strings.Contains(w.Body.String(), "canary") || strings.Contains(w.Body.String(), "hidden") || strings.Contains(w.Body.String(), "12345") || store.legacy.Load() != 0 {
				t.Fatalf("failure leaked partial/private history: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestHistoryDeadlineRejectsLateSuccessAndSharesAdmission(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }
	defer releaseAll()
	store := &historyScopeStore{markers: func(ctx context.Context, c string) ([]graph.SnapshotMeta, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > graph.HistoryReadTimeout {
			t.Error("missing bounded read context")
		}
		entered <- struct{}{}
		<-release
		return []graph.SnapshotMeta{{ClusterID: c}}, nil
	}}
	s := New("", store, aggregator.NewRegistry(), WithSnapshots(time.Hour))
	responses := []*httptest.ResponseRecorder{}
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		r := impactTestRequest(ctx, "/api/v1/snapshots", "")
		w := serveImpactTest(s, r)
		responses = append(responses, w)
		cancel()
		if w.Code != 504 || strings.Contains(w.Body.String(), "snapshots") {
			t.Fatal("expired read returned success")
		}
	}
	if len(entered) != 2 {
		t.Fatal("slow reads did not enter")
	}
	if w := serveHistory(s, "/api/v1/snapshots", ""); w.Code != 429 {
		t.Fatal("abandoned workers released admission too early")
	}
	if w := serveHistory(s, "/api/v1/impact/demo/Pod/name", ""); w.Code != 429 {
		t.Fatal("history allocated a separate analysis concurrency budget")
	}
	releaseAll()
	deadline := time.Now().Add(time.Second)
	for len(s.impactSem) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.impactSem) != 0 {
		t.Fatal("completed workers did not release admission")
	}
	for _, w := range responses {
		if w.Code != 504 || strings.Contains(w.Body.String(), "snapshots") {
			t.Fatal("late worker wrote success after timeout")
		}
	}
}

func TestHistoryOpenAPIV1SelectorDoesNotChangeAlphaContract(t *testing.T) {
	s := New("", memory.New(), aggregator.NewRegistry())
	for _, path := range []string{"snapshots", "snapshots/diff"} {
		alpha := s.OpenAPISpecV1Alpha1()["paths"].(map[string]any)["/api/v1alpha1/"+path].(map[string]any)["get"].(map[string]any)
		before := openAPIOperation(findHistoryRoute(s, path))
		if !reflect.DeepEqual(alpha, before) {
			t.Fatal("frozen alpha operation changed")
		}
		v1 := s.OpenAPISpecV1()["paths"].(map[string]any)["/api/v1/"+path].(map[string]any)["get"].(map[string]any)
		found := false
		for _, p := range v1["parameters"].([]map[string]any) {
			if p["name"] == "cluster" {
				found = true
			}
		}
		if !found {
			t.Fatal("v1 selector absent from schema")
		}
	}
}
func findHistoryRoute(s *Server, path string) RouteInfo {
	for _, r := range s.Routes() {
		if r.Pattern == "/api/v1alpha1/"+path {
			return r
		}
	}
	panic("missing history route")
}

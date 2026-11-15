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
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

type markerRecorderSpy struct {
	graph.GraphStore
	calls  atomic.Int64
	record func(context.Context, string, graph.SnapshotTrigger) (graph.SnapshotMeta, error)
}

func (s *markerRecorderSpy) RecordSnapshotMarker(ctx context.Context, c string, tr graph.SnapshotTrigger) (graph.SnapshotMeta, error) {
	s.calls.Add(1)
	return s.record(ctx, c, tr)
}
func triggerRequest(ctx context.Context, query, token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/_internal/snapshot/trigger"+query, http.NoBody).WithContext(ctx)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}
func TestSnapshotMarkerScopeAuthorizationBeforeAnyWrite(t *testing.T) {
	for _, tc := range []struct {
		name, query, token string
		status             int
		lists              int64
	}{
		{"missing token", "?cluster=prod", "", 401, 0}, {"unknown token", "?cluster=prod", "bad", 403, 0},
		{"hidden member", "?cluster=hidden", "prod-token", 403, 0}, {"absent hidden member", "?cluster=absent", "prod-token", 403, 0},
		{"authorized absent", "?cluster=gone", "gone-token", 404, 1}, {"no selection", "", "prod-token", 400, 0},
		{"empty selection", "?cluster=", "prod-token", 400, 0}, {"repeated member", "?cluster=prod&cluster=prod", "prod-token", 400, 0},
		{"multiple member", "?cluster=prod,hidden", "prod-token", 400, 0}, {"whitespace", "?cluster=%20prod", "prod-token", 400, 0},
		{"invalid separator", "?cluster=prod:other", "prod-token", 400, 0}, {"repeated trigger", "?cluster=prod&trigger=manual&trigger=periodic", "prod-token", 400, 1},
		{"invalid trigger", "?cluster=prod&trigger=bogus", "prod-token", 400, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &markerRecorderSpy{} // A fallback through GraphStore panics and fails the test.
			clusters := &impactAPIClusters{clusters: []string{"prod", "hidden"}}
			s := New("", spy, aggregator.NewRegistry(), WithSnapshots(time.Hour), WithClusterLister(clusters), WithClusterRBAC(historyRBAC()))
			w := serveImpactTest(s, triggerRequest(context.Background(), tc.query, tc.token))
			if w.Code != tc.status || spy.calls.Load() != 0 || clusters.calls.Load() != tc.lists {
				t.Fatalf("code=%d calls=%d lists=%d body=%s", w.Code, spy.calls.Load(), clusters.calls.Load(), w.Body)
			}
			if strings.Contains(w.Body.String(), "hidden") || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Vary") != "Authorization" {
				t.Fatal("scope error exposed private data or lacks cache isolation")
			}
		})
	}
}
func TestSnapshotMarkerExactMemberReceiptAndReadback(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	for _, c := range []string{"", "prod", "hidden"} {
		for i := range 3 {
			if c == "prod" && i > 0 {
				continue
			}
			if err := store.UpsertResource(ctx, graph.Resource{ClusterID: c, Namespace: "demo", Kind: "Pod", Name: string(rune('a' + i))}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, federated := range []bool{true, false} {
		opts := []ServerOption{WithSnapshots(time.Hour)}
		query, token, scope := "", "", ""
		if federated {
			opts = append(opts, WithClusterLister(&impactAPIClusters{clusters: []string{"prod", "hidden"}}), WithClusterRBAC(historyRBAC()))
			query, token, scope = "?cluster=prod&trigger=periodic", "prod-token", "prod"
		}
		s := New("", store, aggregator.NewRegistry(), opts...)
		w := serveImpactTest(s, triggerRequest(ctx, query, token))
		var response SnapshotTriggerResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		want := int64(3)
		if federated {
			want = 1
		}
		if response.ClusterID != scope || response.ResourceCount != want || response.EdgeCount != 0 {
			t.Fatalf("mixed scoped counts: %+v", response)
		}
		markers, err := store.ReadHistoryMarkers(ctx, scope)
		if err != nil || len(markers) != 1 || markers[0].ResourceCount != response.ResourceCount || markers[0].DurationMS != response.DurationMS || string(markers[0].Trigger) != response.Trigger {
			t.Fatal("response is not the persisted receipt", markers, err)
		}
		readQuery := ""
		if federated {
			readQuery = "?cluster=prod"
		}
		read := serveHistory(s, "/api/v1/snapshots"+readQuery, token)
		var listed SnapshotListResponse
		if err := json.Unmarshal(read.Body.Bytes(), &listed); err != nil || read.Code != 200 || listed.Count != 1 || listed.Snapshots[0].ClusterID != scope {
			t.Fatalf("marker cannot be read in same member: %s", read.Body)
		}
	}
	hidden, err := store.ReadHistoryMarkers(ctx, "hidden")
	if err != nil || len(hidden) != 0 {
		t.Fatal("selected trigger wrote a hidden member marker")
	}
}
func TestSnapshotMarkerDisabledUnsupportedAndBusyDoNotWrite(t *testing.T) {
	spy := &markerRecorderSpy{}
	disabled := New("", spy, aggregator.NewRegistry())
	if w := serveImpactTest(disabled, triggerRequest(context.Background(), "", "")); w.Code != 503 || spy.calls.Load() != 0 {
		t.Fatal("disabled snapshots accepted a write")
	}
	legacy := struct{ graph.GraphStore }{nil}
	unsupported := New("", legacy, aggregator.NewRegistry(), WithSnapshots(time.Hour))
	if w := serveImpactTest(unsupported, triggerRequest(context.Background(), "", "")); w.Code != 503 {
		t.Fatal("optional capability used a global fallback")
	}
	busy := New("", spy, aggregator.NewRegistry(), WithSnapshots(time.Hour))
	busy.impactSem <- struct{}{}
	busy.impactSem <- struct{}{}
	if w := serveImpactTest(busy, triggerRequest(context.Background(), "", "")); w.Code != 429 || spy.calls.Load() != 0 {
		t.Fatal("trigger bypassed shared analysis admission")
	}
}
func TestSnapshotMarkerFailureHasNoRetryAndRejectsLateOrInvalidReceipt(t *testing.T) {
	valid := graph.SnapshotMeta{ID: 1, Timestamp: time.Now(), Trigger: graph.SnapshotTriggerManual}
	for _, tc := range []struct {
		name   string
		record func(context.Context, string, graph.SnapshotTrigger) (graph.SnapshotMeta, error)
		status int
	}{
		{"provider error", func(context.Context, string, graph.SnapshotTrigger) (graph.SnapshotMeta, error) {
			return graph.SnapshotMeta{}, errors.New("private-canary")
		}, 500},
		{"provider timeout", func(context.Context, string, graph.SnapshotTrigger) (graph.SnapshotMeta, error) {
			return graph.SnapshotMeta{}, context.DeadlineExceeded
		}, 504},
		{"late success", func(ctx context.Context, _ string, _ graph.SnapshotTrigger) (graph.SnapshotMeta, error) {
			<-ctx.Done()
			return valid, nil
		}, 504},
		{"foreign receipt", func(context.Context, string, graph.SnapshotTrigger) (graph.SnapshotMeta, error) {
			m := valid
			m.ClusterID = "hidden"
			return m, nil
		}, 500},
		{"invalid receipt", func(context.Context, string, graph.SnapshotTrigger) (graph.SnapshotMeta, error) {
			return graph.SnapshotMeta{}, nil
		}, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &markerRecorderSpy{record: func(ctx context.Context, c string, tr graph.SnapshotTrigger) (graph.SnapshotMeta, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > graph.HistoryReadTimeout {
					t.Error("missing bounded store context")
				}
				return tc.record(ctx, c, tr)
			}}
			s := New("", spy, aggregator.NewRegistry(), WithSnapshots(time.Hour))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			w := serveImpactTest(s, triggerRequest(ctx, "", ""))
			if w.Code != tc.status || spy.calls.Load() != 1 || strings.Contains(w.Body.String(), "canary") || strings.Contains(w.Body.String(), "hidden") || !strings.Contains(w.Body.String(), "no automatic retry") {
				t.Fatalf("bad failure: %d %s calls=%d", w.Code, w.Body, spy.calls.Load())
			}
			if len(s.impactSem) != 0 {
				t.Fatal("finished synchronous recorder leaked admission")
			}
		})
	}
}
func TestSnapshotMarkerV1OpenAPIPreservesAlpha(t *testing.T) {
	s := New("", memory.New(), aggregator.NewRegistry())
	path := "/api/_internal/snapshot/trigger"
	alpha := s.OpenAPISpecV1Alpha1()
	var original RouteInfo
	for _, r := range s.Routes() {
		if r.Pattern == path {
			original = r
		}
	}
	operation := alpha["paths"].(map[string]any)[path].(map[string]any)["post"]
	if !reflect.DeepEqual(operation, openAPIOperation(original)) {
		t.Fatal("alpha internal route schema changed")
	}
	v1 := s.OpenAPISpecV1()
	operationV1 := v1["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)
	found := false
	for _, p := range operationV1["parameters"].([]map[string]any) {
		if p["name"] == "cluster" {
			found = true
		}
	}
	if !found {
		t.Fatal("v1 internal trigger lacks member selector")
	}
	schemas := v1["components"].(map[string]any)["schemas"].(map[string]any)
	if _, ok := schemas["SnapshotTriggerResponseV1"].(map[string]any)["properties"].(map[string]any)["clusterId"]; !ok {
		t.Fatal("v1 member receipt missing")
	}
	alphaSchema := alpha["components"].(map[string]any)["schemas"].(map[string]any)["SnapshotTriggerResponse"]
	if !reflect.DeepEqual(alphaSchema, schemas["SnapshotTriggerResponse"]) {
		t.Fatal("frozen response schema changed")
	}
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package historyclient

import (
	"context"
	"encoding/json"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHistoryClientExactQueryMetadataAndOneRequest(t *testing.T) {
	for _, diff := range []bool{false, true} {
		t.Run(map[bool]string{false: "list", true: "diff"}[diff], func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Query().Get("cluster") != "east%prod" || len(r.URL.Query()["cluster"]) != 1 || r.Header.Get("Authorization") != "Bearer token-canary" || r.URL.Query().Has("token") {
					t.Error("request scope/auth mismatch")
				}
				w.Header().Set("Content-Type", "application/json")
				if diff {
					if r.URL.Path != "/proxy/api/v1/snapshots/diff" || r.URL.Query().Get("namespace") != "demo" {
						t.Error("diff endpoint mismatch")
					}
					_, _ = w.Write([]byte(`{"from":"2026-10-20T00:00:00Z","to":"2026-10-20T01:00:00Z","added":[],"removed":[],"modified":[{"namespace":"demo","kind":"Pod","name":"observed","uid":"uid","eventType":"update","ts":"2026-10-20T00:30:00Z"}]}`))
				} else {
					if r.URL.Path != "/proxy/api/v1/snapshots" {
						t.Error("list endpoint mismatch")
					}
					_ = json.NewEncoder(w).Encode(ListResponse{Snapshots: []graph.SnapshotMeta{{ClusterID: "east%prod", Timestamp: time.Now(), ResourceCount: 7, Trigger: graph.SnapshotTriggerManual}}, Count: 1})
				}
			}))
			defer server.Close()
			q := Query{ClusterID: "east%prod", Diff: diff}
			if diff {
				q.From = "2026-10-20T00:00:00Z"
				q.To = "2026-10-20T01:00:00Z"
				q.Namespace = "demo"
			}
			body, err := Fetch(context.Background(), server.URL+"/proxy", "token-canary", q)
			if err != nil || !json.Valid(body) || calls.Load() != 1 || strings.Contains(string(body), "canary") {
				t.Fatalf("read failed: %v %s", err, body)
			}
		})
	}
}
func TestHistoryClientRejectsErrorsRedirectsAndInvalidMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, body, content string
		status              int
		diff                bool
	}{
		{name: "denied", status: 403, body: "private-server-canary"}, {name: "unavailable", status: 503, body: "private-server-canary"}, {name: "oversize", status: 200, content: "application/json", body: strings.Repeat("x", graph.MaxHistoryBytes+1)},
		{name: "wrong scope", status: 200, content: "application/json", body: `{"snapshots":[{"clusterId":"hidden-canary"}],"count":1}`},
		{name: "wrong count", status: 200, content: "application/json", body: `{"snapshots":[],"count":9}`},
		{name: "payload field", status: 200, content: "application/json", body: `{"snapshots":[],"count":0,"data":"private-server-canary"}`},
		{name: "content type", status: 200, content: "text/html", body: "private-server-canary"},
		{name: "trailing body", status: 200, content: "application/json", body: `{"snapshots":[],"count":0}{}`},
		{name: "foreign namespace", status: 200, content: "application/json", diff: true, body: `{"from":"2026-10-20T00:00:00Z","to":"2026-10-20T01:00:00Z","added":[{"namespace":"other","kind":"Pod","name":"private-server-canary","eventType":"add","ts":"2026-10-20T00:30:00Z"}],"removed":[],"modified":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", tc.content)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			q := Query{ClusterID: "east", Diff: tc.diff}
			if tc.diff {
				q.From = "1h"
				q.Namespace = "demo"
			}
			body, err := Fetch(context.Background(), s.URL, "token-canary", q)
			if err == nil || body != nil || calls != 1 || strings.Contains(err.Error(), "canary") {
				t.Fatalf("invalid response accepted/leaked: %v", err)
			}
		})
	}
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/canary-redirect-target", http.StatusFound)
	}))
	defer s.Close()
	if body, err := Fetch(context.Background(), s.URL, "", Query{}); err == nil || body != nil || calls != 1 {
		t.Fatal("redirect followed or accepted")
	}
}
func TestHistoryClientInvalidInputsAndCancellation(t *testing.T) {
	for _, q := range []Query{{ClusterID: "east,west"}, {ClusterID: " east"}, {ClusterID: "east:other"}, {Diff: true}, {Diff: true, From: "now", To: "1h"}, {Diff: true, From: "not-time"}, {Namespace: "demo"}} {
		if err := q.Validate(); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	for _, base := range []string{"http://example.test", "http://localhost:8080", "https://user:password-canary@example.test", "https://example.test?token=canary", "https://example.test/#canary", "https://example.test/../"} {
		body, err := Fetch(context.Background(), base, "", Query{})
		if err == nil || body != nil || strings.Contains(err.Error(), "canary") {
			t.Fatal("unsafe/ambiguous base accepted or leaked")
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Fetch(cancelled, "http://127.0.0.1:1", "token-canary", Query{}); err == nil || strings.Contains(err.Error(), "canary") {
		t.Fatal("cancelled read accepted")
	}
}

func TestHistoryClientRejectsMismatchedAbsoluteWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"from":"2026-10-19T00:00:00Z","to":"2026-10-20T01:00:00Z","added":[],"removed":[],"modified":[]}`))
	}))
	defer server.Close()
	if body, err := Fetch(context.Background(), server.URL, "", Query{Diff: true, From: "2026-10-20T00:00:00Z", To: "2026-10-20T01:00:00Z"}); err == nil || body != nil {
		t.Fatal("different absolute time window accepted")
	}
}

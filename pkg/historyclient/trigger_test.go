// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package historyclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func triggerBody(cluster string) string {
	data, _ := json.Marshal(TriggerReceipt{ClusterID: cluster, Trigger: graph.SnapshotTriggerManual, ResourceCount: 2, EdgeCount: 1})
	return string(data)
}
func TestTriggerURLTrustAndScope(t *testing.T) {
	q := TriggerQuery{ClusterID: "east", Trigger: graph.SnapshotTriggerManual}
	for _, base := range []string{"https://operator.example/base", "http://127.0.0.1:8080", "http://[::1]:8080", "http://release.ops.svc:80", "http://release.ops.svc.cluster.local:80"} {
		got, err := q.requestURL(base)
		if err != nil || !strings.Contains(got, "/api/_internal/snapshot/trigger?cluster=east&trigger=manual") {
			t.Fatalf("%s: %s %v", base, got, err)
		}
	}
	for _, base := range []string{"http://localhost:8080", "http://example.com", "http://release.svc", "http://release.ops.svc.evil", "http://release.ops.evil.svc", "http://-release.ops.svc", "http://release..svc", "http://release.OPS.svc", "http://10.0.0.1", "https://user:private@example.com", "https://example.com?token=private", "https://example.com#fragment", "https://example.com/base/../other", "https://example.com/%2e%2e/other", "ftp://example.com", "https://example.com?"} {
		if _, err := q.requestURL(base); err == nil {
			t.Fatalf("unsafe base accepted: %s", base)
		}
	}
	for _, q := range []TriggerQuery{{ClusterID: "east,west", Trigger: graph.SnapshotTriggerManual}, {ClusterID: " east", Trigger: graph.SnapshotTriggerManual}, {ClusterID: "bad:scope", Trigger: graph.SnapshotTriggerManual}, {Trigger: "bogus"}} {
		if _, err := q.requestURL("http://127.0.0.1"); err == nil {
			t.Fatal("invalid marker selection accepted")
		}
	}
}
func TestTriggerOnePOSTAuthAndValidatedReceipt(t *testing.T) {
	for _, scope := range []string{"", "east"} {
		t.Run(scope, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/api/_internal/snapshot/trigger" || r.URL.Query().Get("cluster") != scope || r.URL.Query().Get("trigger") != "manual" || r.Header.Get("Authorization") != "Bearer token-canary" || r.ContentLength != 0 {
					t.Error("mutation scope, credentials or method changed")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(triggerBody(scope)))
			}))
			defer server.Close()
			out, err := Trigger(context.Background(), server.URL, "token-canary", TriggerQuery{ClusterID: scope, Trigger: graph.SnapshotTriggerManual})
			var receipt TriggerReceipt
			if err != nil || json.Unmarshal(out, &receipt) != nil || receipt.ClusterID != scope || receipt.ResourceCount != 2 || receipt.EdgeCount != 1 || calls.Load() != 1 {
				t.Fatalf("receipt: %s %v calls=%d", out, err, calls.Load())
			}
		})
	}
}
func TestTriggerRejectsUntrustedReceiptsWithoutRetry(t *testing.T) {
	valid := triggerBody("east")
	cases := map[string]string{
		"foreign member": triggerBody("hidden"), "missing member": triggerBody(""),
		"wrong trigger":     strings.Replace(valid, "manual", "periodic", 1),
		"negative count":    strings.Replace(valid, `"resourceCount":2`, `"resourceCount":-1`, 1),
		"fraction":          strings.Replace(valid, `"resourceCount":2`, `"resourceCount":1.5`, 1),
		"missing count":     strings.Replace(valid, `"resourceCount":2,`, "", 1),
		"null count":        strings.Replace(valid, `"resourceCount":2`, `"resourceCount":null`, 1),
		"unknown field":     strings.TrimSuffix(valid, "}") + `,"private":"payload-canary"}`,
		"duplicate count":   strings.TrimSuffix(valid, "}") + `,"edgeCount":99}`,
		"escaped duplicate": strings.TrimSuffix(valid, "}") + `,"edge\u0043ount":99}`,
		"case alias":        strings.TrimSuffix(valid, "}") + `,"RESOURCECOUNT":99}`,
		"duplicate member":  strings.TrimSuffix(valid, "}") + `,"clusterId":"east"}`,
		"null member":       strings.Replace(valid, `"clusterId":"east"`, `"clusterId":null`, 1),
		"trailing":          valid + `{}`, "large": strings.Repeat(" ", MaxTriggerReceiptBytes+1) + valid,
		"invalid utf8": string([]byte{0xff}), "array": "[]",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			out, err := Trigger(context.Background(), server.URL, "", TriggerQuery{ClusterID: "east", Trigger: graph.SnapshotTriggerManual})
			if err == nil || out != nil || calls.Load() != 1 || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "hidden") || !strings.Contains(err.Error(), "no automatic retry") {
				t.Fatalf("bad receipt accepted/leaked/retried: %s %v calls=%d", out, err, calls.Load())
			}
		})
	}
}
func TestTriggerRedirectAndStatusDoNotReplayOrEcho(t *testing.T) {
	var redirected atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				_, _ = w.Write([]byte("private-provider-canary"))
			}))
			defer server.Close()
			out, err := Trigger(context.Background(), server.URL, "private-token", TriggerQuery{Trigger: graph.SnapshotTriggerManual})
			var failure *TriggerStatusError
			if out != nil || !errors.As(err, &failure) || failure.Status != status || strings.Contains(err.Error(), "private") || calls.Load() != 1 || redirected.Load() != 0 {
				t.Fatalf("response replayed/disclosed: %s %v", out, err)
			}
		})
	}
}
func TestTriggerCancellationAndInputRejection(t *testing.T) {
	var calls atomic.Int64
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	q := TriggerQuery{Trigger: graph.SnapshotTriggerManual}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := Trigger(cancelled, server.URL, "", q); out != nil || err == nil || calls.Load() != 0 {
		t.Fatal("cancelled request was sent")
	}
	if out, err := Trigger(context.Background(), server.URL, "token\ncanary", q); out != nil || err == nil || calls.Load() != 0 {
		t.Fatal("invalid credential was sent")
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	result := make(chan error, 1)
	go func() {
		out, err := Trigger(ctx, server.URL, "", q)
		if out != nil {
			t.Error("cancelled mutation returned a receipt")
		}
		result <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("request did not reach the controlled handler")
	}
	stop()
	select {
	case err := <-result:
		if err == nil || calls.Load() != 1 {
			t.Fatal("cancelled mutation was retried or accepted", err, calls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("request ignored cancellation")
	}
}

func TestTriggerLostOrNonJSONReceiptDoesNotRetry(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(map[bool]string{false: "content type", true: "truncated acknowledgement"}[truncated], func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if truncated {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Content-Length", "1000")
				} else {
					w.Header().Set("Content-Type", "text/plain")
				}
				_, _ = w.Write([]byte(triggerBody("")))
			}))
			defer server.Close()
			out, err := Trigger(context.Background(), server.URL, "", TriggerQuery{Trigger: graph.SnapshotTriggerManual})
			if err == nil || out != nil || calls.Load() != 1 || !strings.Contains(err.Error(), "outcome may be unknown") {
				t.Fatal("lost receipt accepted or write repeated", out, err, calls.Load())
			}
		})
	}
}

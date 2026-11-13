// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSnapshotReadCommandScopeAuthOutputAndNoFallback(t *testing.T) {
	t.Setenv("KUBEATLAS_TOKEN", "token-canary")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Query().Get("cluster") != "east" || r.Header.Get("Authorization") != "Bearer token-canary" {
			t.Error("scope/auth mismatch")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"snapshots":[],"count":0}`))
	}))
	defer server.Close()
	var out, errOut bytes.Buffer
	if status := runSnapshotRead(context.Background(), []string{"--server", server.URL, "--cluster", "east"}, false, &out, &errOut); status != 0 || !strings.Contains(out.String(), `"snapshots"`) || errOut.Len() != 0 || calls != 1 {
		t.Fatalf("read: %d %s", status, errOut.String())
	}
	for _, args := range [][]string{{"--cluster", "east", "--cluster", "west"}, {"--cluster="}, {"--cluster=east,west"}, {"--from", "1h"}, {"positional"}, {"--timeout", "0s"}} {
		out.Reset()
		errOut.Reset()
		before := calls
		if status := runSnapshotRead(context.Background(), append([]string{"--server", server.URL}, args...), false, &out, &errOut); status == 0 || calls != before || out.Len() != 0 {
			t.Fatal("invalid input issued read or output")
		}
	}
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "private-server-canary", http.StatusForbidden)
	}))
	defer denied.Close()
	out.Reset()
	errOut.Reset()
	if status := runSnapshotRead(context.Background(), []string{"--server", denied.URL, "--cluster", "east"}, false, &out, &errOut); status == 0 || out.Len() != 0 || strings.Contains(errOut.String(), "canary") {
		t.Fatal("denied response exported or disclosed")
	}
}

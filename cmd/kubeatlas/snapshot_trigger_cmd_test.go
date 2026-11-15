// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSnapshotTriggerCommandScopeAuthAndNoInvalidDispatch(t *testing.T) {
	t.Setenv("KUBEATLAS_TOKEN", "token-canary")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Query().Get("cluster") != "east" || r.URL.Query().Get("trigger") != "periodic" || r.Header.Get("Authorization") != "Bearer token-canary" {
			t.Error("member trigger scope/auth mismatch")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"clusterId":"east","trigger":"periodic","resourceCount":2,"edgeCount":1,"durationMs":0}`))
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	if code := runSnapshotTriggerCommand(context.Background(), []string{"--server", server.URL, "--cluster", "east", "--trigger", "periodic"}, &out, &diagnostics); code != 0 || !strings.Contains(out.String(), `"clusterId":"east"`) || diagnostics.Len() != 0 || calls != 1 {
		t.Fatalf("trigger: %d %s %s", code, out.String(), diagnostics.String())
	}
	for _, args := range [][]string{{"--cluster=east,west"}, {"--cluster="}, {"--cluster=east", "--cluster=west"}, {"--trigger=bogus"}, {"--trigger=manual", "--trigger=periodic"}, {"--server", server.URL}, {"--timeout=0s"}, {"--timeout=1m"}, {"--timeout=1s", "--timeout=2s"}, {"positional"}} {
		out.Reset()
		diagnostics.Reset()
		before := calls
		if code := runSnapshotTriggerCommand(context.Background(), append([]string{"--server", server.URL}, args...), &out, &diagnostics); code != 1 || calls != before || out.Len() != 0 {
			t.Fatalf("invalid flags caused a write: %v code=%d %s", args, code, diagnostics.String())
		}
	}
}

type triggerBrokenOutput struct{}

func (triggerBrokenOutput) Write([]byte) (int, error) { return 0, errors.New("output-canary") }

func TestSnapshotTriggerCommandOutcomeAndExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		broken bool
		code   int
	}{
		{"denied", http.StatusForbidden, "private-provider-canary", false, 2},
		{"server error", http.StatusInternalServerError, "private-provider-canary", false, 2},
		{"foreign receipt", http.StatusOK, `{"clusterId":"hidden","trigger":"manual","resourceCount":1,"edgeCount":0,"durationMs":0}`, false, 1},
		{"output failure", http.StatusOK, `{"trigger":"manual","resourceCount":1,"edgeCount":0,"durationMs":0}`, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			var out, diagnostics bytes.Buffer
			var code int
			if tc.broken {
				code = runSnapshotTriggerCommand(context.Background(), []string{"--server", server.URL}, triggerBrokenOutput{}, &diagnostics)
			} else {
				code = runSnapshotTriggerCommand(context.Background(), []string{"--server", server.URL}, &out, &diagnostics)
			}
			if code != tc.code || calls != 1 || out.Len() != 0 || strings.Contains(diagnostics.String(), "canary") || strings.Contains(diagnostics.String(), "hidden") || !strings.Contains(diagnostics.String(), "no automatic retry") {
				t.Fatalf("bad outcome: %d calls=%d %s", code, calls, diagnostics.String())
			}
		})
	}
}

func TestSnapshotTriggerHelpDoesNotEchoEnvironmentCredentials(t *testing.T) {
	t.Setenv("KUBEATLAS_URL", "https://user:credential-canary@server.invalid?token=query-canary")
	t.Setenv("KUBEATLAS_TOKEN", "token-canary")
	var out, diagnostics bytes.Buffer
	if code := runSnapshotTriggerCommand(context.Background(), []string{"--help"}, &out, &diagnostics); code != 1 || out.Len() != 0 || strings.Contains(diagnostics.String(), "canary") || strings.Contains(diagnostics.String(), "panic") || !strings.Contains(diagnostics.String(), "cluster") {
		t.Fatalf("unsafe help output: %d %s", code, diagnostics.String())
	}
}

func TestSnapshotTriggerFlagErrorDoesNotEchoCredentialURL(t *testing.T) {
	var out, diagnostics bytes.Buffer
	code := runSnapshotTriggerCommand(context.Background(), []string{"--server=https://first.invalid", "--server=https://user:credential-canary@second.invalid"}, &out, &diagnostics)
	if code != 1 || out.Len() != 0 || strings.Contains(diagnostics.String(), "canary") || strings.Contains(diagnostics.String(), "second.invalid") {
		t.Fatalf("credential URL leaked: %d %s", code, diagnostics.String())
	}
}

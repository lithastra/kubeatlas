// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/impact"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func commandImpactResponse(t *testing.T) *impact.Response {
	t.Helper()
	store := memory.New()
	root := graph.Resource{Namespace: "demo", Kind: "ConfigMap", Name: "settings", UID: "root-uid"}
	if err := store.UpsertResource(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	a, err := analysis.AnalyzeImpactWithEvidence(context.Background(), store, root.ID(), analysis.ImpactOptions{}, graph.ImpactSnapshotOptions{}, analysis.ImpactEvidenceProviders{})
	if err != nil {
		t.Fatal(err)
	}
	return &impact.Response{SchemaVersion: "1", KubeAtlasVersion: "1.7.0-dev", GeneratedAt: time.Now().UTC(),
		Scope: impact.Scope{Mode: "single", NamespaceScope: "all_namespaces_in_selected_cluster"}, Analysis: a, SharingWarning: impact.SharingWarning}
}

func executeImpact(t *testing.T, a *app, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCmd(a)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestImpactCommandOutputsAndClosesTunnel(t *testing.T) {
	t.Setenv("KUBEATLAS_TOKEN", "synthetic-bearer")
	r := commandImpactResponse(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer synthetic-bearer" || req.URL.Path != "/api/v1/impact/demo/ConfigMap/settings" {
			t.Error("wrong request")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(r)
	}))
	defer server.Close()
	for _, format := range []string{"text", "json", "html"} {
		t.Run(format, func(t *testing.T) {
			a, opened := newTestApp()
			closed := false
			a.resolve = func(ctx context.Context, value, namespace string, k kubeFlags) (string, func(), bool, error) {
				if _, ok := ctx.Deadline(); !ok || value != "https://selected.example" || k.context != "selected-context" {
					t.Error("missing deadline/server/context")
				}
				return server.URL, func() { closed = true }, true, nil
			}
			stdout, stderr, err := executeImpact(t, a, "impact", "ConfigMap", "settings", "-n", "demo", "--output", format,
				"--server", "https://selected.example", "--context", "selected-context", "--expected-uid", "root-uid")
			if err != nil || !closed || *opened != "" || stderr != "" || strings.Contains(stdout, "synthetic-bearer") {
				t.Fatalf("output=%s err=%v stderr=%s closed=%v opened=%s", format, err, stderr, closed, *opened)
			}
			if format == "json" && !json.Valid([]byte(stdout)) {
				t.Fatal("stdout was not pure JSON")
			}
			if format == "text" && !strings.Contains(stdout, "analysis incomplete") {
				t.Fatal("empty result was not qualified")
			}
		})
	}
}

func TestImpactCommandPrivateFileNoOverwrite(t *testing.T) {
	r := commandImpactResponse(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(r)
	}))
	defer server.Close()
	newApp := func() *app {
		a, _ := newTestApp()
		a.resolve = func(context.Context, string, string, kubeFlags) (string, func(), bool, error) {
			return server.URL, noopCleanup, false, nil
		}
		return a
	}
	out := filepath.Join(t.TempDir(), "impact.json")
	args := []string{"impact", "ConfigMap", "settings", "-n", "demo", "-o", "json", "--out", out}
	stdout, stderr, err := executeImpact(t, newApp(), args...)
	if err != nil || stdout != "" || !strings.Contains(stderr, "sensitive cluster topology") {
		t.Fatalf("file output err=%v stdout=%s stderr=%s", err, stdout, stderr)
	}
	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("unsafe file mode: %v", err)
	}
	before, _ := os.ReadFile(out)
	if _, _, err := executeImpact(t, newApp(), args...); err == nil {
		t.Fatal("overwrote existing report")
	}
	after, _ := os.ReadFile(out)
	if !bytes.Equal(before, after) {
		t.Fatal("existing file changed")
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(out, link); err != nil {
		t.Fatal(err)
	}
	args[len(args)-1] = link
	if _, _, err := executeImpact(t, newApp(), args...); err == nil {
		t.Fatal("followed existing output symlink")
	}
}

func TestImpactCommandInvalidArgumentsNeverResolve(t *testing.T) {
	for _, flags := range [][]string{
		{"--relation", "both"}, {"--max-depth", "0"}, {"--max-depth", "11"}, {"--limit", "0"}, {"--limit", "1001"},
		{"--output", "svg"}, {"--timeout", "0s"}, {"--timeout", "3m"}, {"--local-ui"}, {"--host", "0.0.0.0"},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			a, _ := newTestApp()
			a.resolve = func(context.Context, string, string, kubeFlags) (string, func(), bool, error) {
				t.Fatal("invalid arguments reached discovery")
				return "", noopCleanup, false, nil
			}
			args := append([]string{"impact", "ConfigMap", "settings", "-n", "demo"}, flags...)
			stdout, _, err := executeImpact(t, a, args...)
			if err == nil || stdout != "" {
				t.Fatal("invalid command emitted a result")
			}
		})
	}
}

func TestImpactCommandFailureDoesNotCreateReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(409)
		w.Write([]byte("private-upstream-canary"))
	}))
	defer server.Close()
	a, _ := newTestApp()
	closed := false
	a.resolve = func(context.Context, string, string, kubeFlags) (string, func(), bool, error) {
		return server.URL, func() { closed = true }, true, nil
	}
	out := filepath.Join(t.TempDir(), "absent.json")
	stdout, _, err := executeImpact(t, a, "impact", "ConfigMap", "settings", "-n", "demo", "--out", out)
	if err == nil || !closed || stdout != "" || strings.Contains(err.Error(), "canary") {
		t.Fatalf("unsafe failure err=%v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("failed request created a report")
	}
}

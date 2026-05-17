// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// dotInstalled reports whether the graphviz `dot` binary is on PATH.
func dotInstalled() bool {
	_, err := exec.LookPath("dot")
	return err == nil
}

// A bad format is a 400 — caught before any rendering work.
func TestExport_RejectsUnknownFormat(t *testing.T) {
	base, _, stop := seedAndServe(t, nil)
	defer stop()

	resp, err := http.Get(base + "/api/v1alpha1/export?format=pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// A view past the node cap (exportMaxNodes = 1000) is a 413.
func TestExport_TooLargeView(t *testing.T) {
	base, _, stop := seedAndServe(t, func(s graph.GraphStore) {
		ctx := context.Background()
		for i := 0; i < 1001; i++ {
			_ = s.UpsertResource(ctx, graph.Resource{
				Kind: "ConfigMap", Namespace: "big", Name: fmt.Sprintf("cm-%04d", i),
			})
		}
	})
	defer stop()

	resp, err := http.Get(base + "/api/v1alpha1/export?namespace=big")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
}

// A small view renders to an SVG when graphviz is available; without
// it the endpoint degrades to 503 rather than crashing.
func TestExport_RendersSVG(t *testing.T) {
	base, _, stop := seedAndServe(t, func(s graph.GraphStore) {
		ctx := context.Background()
		dep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "api"}
		cm := graph.Resource{Kind: "ConfigMap", Namespace: "demo", Name: "api-config"}
		_ = s.UpsertResource(ctx, dep)
		_ = s.UpsertResource(ctx, cm)
		_ = s.UpsertEdge(ctx, graph.Edge{From: dep.ID(), To: cm.ID(), Type: graph.EdgeTypeUsesConfigMap})
	})
	defer stop()

	resp, err := http.Get(base + "/api/v1alpha1/export?format=svg&namespace=demo")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if !dotInstalled() {
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (graphviz absent)", resp.StatusCode)
		}
		return
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
	if !bytes.Contains(body, []byte("<svg")) {
		t.Errorf("body does not look like SVG: %.80q", body)
	}
}

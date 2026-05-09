// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// TestExport_RejectsUnsupportedFormat ensures the SVG / mermaid
// anti-patterns are enforced at the CLI layer.
func TestExport_RejectsUnsupportedFormat(t *testing.T) {
	for _, fmt := range []string{"svg", "mermaid", "json"} {
		t.Run(fmt, func(t *testing.T) {
			code := runExport([]string{"--format=" + fmt})
			if code == 0 {
				t.Errorf("--format=%s: got exit 0, want non-zero", fmt)
			}
		})
	}
}

// TestExport_DOTRendersThroughGraphviz round-trips the renderer
// output through `dot -Tsvg` when graphviz is on PATH. Skips
// otherwise so CI without graphviz still passes.
func TestExport_DOTRendersThroughGraphviz(t *testing.T) {
	dotBin, err := exec.LookPath("dot")
	if err != nil {
		t.Skipf("graphviz not installed; skipping (PATH lookup: %v)", err)
	}
	g := &graph.Graph{
		Resources: []graph.Resource{
			{Kind: "Deployment", Namespace: "demo", Name: "web"},
			{Kind: "ConfigMap", Namespace: "demo", Name: "cm"},
		},
		Edges: []graph.Edge{
			{From: "demo/Deployment/web", To: "demo/ConfigMap/cm", Type: graph.EdgeTypeUsesConfigMap},
		},
	}
	dotInput := graph.ToDOT(g)

	cmd := exec.Command(dotBin, "-Tsvg")
	cmd.Stdin = strings.NewReader(dotInput)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("dot -Tsvg: %v\nstderr: %s\ninput:\n%s", err, stderr.String(), dotInput)
	}
	out := stdout.String()
	if !strings.Contains(out, "<svg") {
		t.Errorf("graphviz output missing <svg>:\n%s", out)
	}
	for _, want := range []string{"web", "cm", "USES_CONFIGMAP"} {
		if !strings.Contains(out, want) {
			t.Errorf("SVG missing %q", want)
		}
	}
}

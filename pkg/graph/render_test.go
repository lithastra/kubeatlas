// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"
)

// sampleGraph is a tiny two-node graph used by the render tests.
func sampleGraph() *Graph {
	dep := Resource{Kind: "Deployment", Namespace: "demo", Name: "api"}
	cm := Resource{Kind: "ConfigMap", Namespace: "demo", Name: "api-config"}
	return &Graph{
		Resources: []Resource{dep, cm},
		Edges:     []Edge{{From: dep.ID(), To: cm.ID(), Type: EdgeTypeUsesConfigMap}},
	}
}

// dotAvailable reports whether the graphviz `dot` binary is on PATH.
func dotAvailable() bool {
	_, err := exec.LookPath("dot")
	return err == nil
}

func TestToImage_RejectsUnknownFormat(t *testing.T) {
	if _, err := ToImage(context.Background(), sampleGraph(), "jpeg", DOTOptions{}); err == nil {
		t.Fatal(`ToImage accepted format "jpeg", want an error`)
	}
}

func TestToImage_GraphvizNotFound(t *testing.T) {
	// An empty PATH makes exec.LookPath("dot") fail deterministically.
	t.Setenv("PATH", "")
	_, err := ToImage(context.Background(), sampleGraph(), "svg", DOTOptions{})
	if !errors.Is(err, ErrGraphvizNotFound) {
		t.Fatalf("err = %v, want ErrGraphvizNotFound", err)
	}
}

func TestToSVG_ProducesSVG(t *testing.T) {
	if !dotAvailable() {
		t.Skip("graphviz 'dot' not installed")
	}
	out, err := ToSVG(context.Background(), sampleGraph(), DOTOptions{})
	if err != nil {
		t.Fatalf("ToSVG: %v", err)
	}
	if !bytes.Contains(out, []byte("<svg")) {
		t.Errorf("output does not look like SVG: %.80q", out)
	}
}

func TestToPNG_ProducesPNG(t *testing.T) {
	if !dotAvailable() {
		t.Skip("graphviz 'dot' not installed")
	}
	out, err := ToPNG(context.Background(), sampleGraph(), DOTOptions{})
	if err != nil {
		t.Fatalf("ToPNG: %v", err)
	}
	// A PNG file starts with the 8-byte signature \x89PNG\r\n\x1a\n.
	if !bytes.HasPrefix(out, []byte("\x89PNG\r\n\x1a\n")) {
		t.Errorf("output is not a PNG (bad signature)")
	}
}

func TestToImage_NamespaceFilterShrinksOutput(t *testing.T) {
	if !dotAvailable() {
		t.Skip("graphviz 'dot' not installed")
	}
	g := &Graph{Resources: []Resource{
		{Kind: "ConfigMap", Namespace: "demo", Name: "a"},
		{Kind: "ConfigMap", Namespace: "other", Name: "b"},
	}}
	scoped, err := ToSVG(context.Background(), g, DOTOptions{Namespace: "demo"})
	if err != nil {
		t.Fatalf("ToSVG: %v", err)
	}
	// The "demo" render must mention demo/a and not other/b.
	if !bytes.Contains(scoped, []byte("demo/ConfigMap/a")) {
		t.Error("namespace-scoped SVG missing the in-scope resource")
	}
	if bytes.Contains(scoped, []byte("other/ConfigMap/b")) {
		t.Error("namespace-scoped SVG leaked an out-of-scope resource")
	}
}

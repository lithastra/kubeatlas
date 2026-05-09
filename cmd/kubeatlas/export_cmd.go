// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

// runExport is the `kubeatlas export` subcommand: stand up a one-shot
// discovery pass against the cluster the current KUBECONFIG points
// at, run every built-in extractor, and emit the result as DOT.
//
// The intent is "permanent escape hatch": the GUI is the primary
// surface for v1.0+, but `kubeatlas export --format=dot | dot -Tsvg`
// keeps a CI-friendly, self-contained path open for users who want
// a graph artifact without standing up the server.
//
// Exit codes:
//
//	0 — DOT written to --output (or stdout) successfully.
//	1 — usage / connection error.
//
// Anti-patterns guarded:
//
//   - --format=svg is intentionally NOT supported; cgo graphviz
//     bindings would bloat the binary. Pipe to `dot -Tsvg` instead.
//   - --format=mermaid is intentionally NOT supported; v1.0 retired
//     the Mermaid path on the API side too.
//   - Default has no --server flag because the server may not exist
//     on the operator's machine; cluster-direct discovery is the
//     most universal default.
func runExport(args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	format := fs.String("format", "dot",
		"Output format. Only 'dot' is supported; pipe through "+
			"`dot -Tsvg` for SVG, `dot -Tpng` for PNG, etc.")
	namespace := fs.String("namespace", "",
		"Restrict the output to one namespace. Empty = whole "+
			"cluster (every resource the operator's RBAC permits).")
	output := fs.String("output", "",
		"Path to write DOT to. Defaults to stdout.")
	title := fs.String("title", "",
		"Override the digraph identifier. Default 'KubeAtlas'.")

	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *format != "dot" {
		fmt.Fprintf(os.Stderr,
			"export: unsupported --format=%q (only 'dot' is supported; "+
				"pipe through Graphviz for other formats)\n", *format)
		return 1
	}

	ctx := context.Background()
	g, err := buildExportGraph(ctx, *namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: %v\n", err)
		return 1
	}

	dot := graph.ToDOTOptions(g, graph.DOTOptions{
		Namespace: *namespace,
		Title:     *title,
	})

	var out io.Writer = os.Stdout
	if *output != "" {
		f, err := os.Create(*output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "export: %v\n", err)
			return 1
		}
		defer f.Close()
		out = f
	}
	if _, err := io.WriteString(out, dot); err != nil {
		fmt.Fprintf(os.Stderr, "export: %v\n", err)
		return 1
	}
	return 0
}

// buildExportGraph assembles a Graph by walking the cluster the
// current KUBECONFIG points at. Returns the in-memory graph
// directly — no store needed since we only need a one-shot dump.
//
// The namespace argument is informational only; pkg/discovery's
// CollectAll always pulls every namespace the RBAC permits, and
// the DOT renderer applies the actual namespace filter so cross-
// namespace edges drop cleanly. Pulling the whole cluster keeps
// the CLI simple and matches the playbook's "default connects to
// the cluster" expectation.
func buildExportGraph(_ context.Context, _ string) (*graph.Graph, error) {
	client, err := discovery.NewClient()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	resources, err := client.CollectAll()
	if err != nil {
		return nil, fmt.Errorf("collect resources: %w", err)
	}

	reg := extractor.Default()
	var edges []graph.Edge
	for _, r := range resources {
		edges = append(edges, reg.ExtractAll(r, resources)...)
	}
	return &graph.Graph{Resources: resources, Edges: edges}, nil
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lithastra/kubeatlas/pkg/collect"
	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/store"
	"github.com/lithastra/kubeatlas/pkg/version"
)

// runDiagnose is the `kubeatlas diagnose` subcommand (F-301): scan the
// cluster the current KUBECONFIG points at — no running kubeatlas
// server needed — and emit a self-contained diagnostic report (full
// dependency graph + orphans + cycles + top blast radius) as HTML or
// JSON.
//
// The offline, server-independent path is deliberate: the core use
// case is an air-gapped audit where the operator wants a portable
// snapshot without standing up the server. The equivalent server-side
// endpoint is GET /api/v1/diagnose.
//
// Exit codes:
//
//	0 — report written to --output (or stdout) successfully.
//	1 — usage / connection / render error.
func runDiagnose(args []string) int {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	namespace := fs.String("namespace", "",
		"Restrict the report to one namespace. Empty (or --all-namespaces) reports the whole cluster.")
	allNS := fs.Bool("all-namespaces", false,
		"Report every namespace (whole-cluster scope). Mutually exclusive with --namespace.")
	output := fs.String("output", "",
		"Path to write the report to. Defaults to stdout.")
	format := fs.String("format", "html",
		"Output format: html (default) | json.")
	// Short aliases bound to the same targets — the documented
	// acceptance commands use `-n` / `-o`.
	fs.StringVar(namespace, "n", "", "Shorthand for --namespace.")
	fs.StringVar(output, "o", "", "Shorthand for --output.")
	kubeconfig := fs.String("kubeconfig", "",
		"Path to the kubeconfig file (overrides $KUBECONFIG).")
	kubeContext := fs.String("context", "",
		"kubeconfig context to use.")

	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *format != "html" && *format != "json" {
		fmt.Fprintf(os.Stderr, "diagnose: unsupported --format=%q (want html | json)\n", *format)
		return 1
	}
	if *allNS && *namespace != "" {
		fmt.Fprintln(os.Stderr, "diagnose: --all-namespaces and --namespace are mutually exclusive")
		return 1
	}

	ctx := context.Background()

	// Tier 1 throwaway store — the report is a one-shot snapshot, so an
	// in-memory store is the right backend regardless of how the
	// cluster's server is configured.
	st, err := store.New(ctx, store.Config{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "diagnose: %v\n", err)
		return 1
	}
	// collect.Cluster runs the offline scan — the same code path the
	// `export` subcommand and the kubectl-atlas plugin use.
	if err := collect.Cluster(ctx, st, *kubeconfig, *kubeContext); err != nil {
		fmt.Fprintf(os.Stderr, "diagnose: offline scan: %v\n", err)
		return 1
	}

	scope := analysis.DiagnoseScope{Namespace: *namespace, AllNamespaces: *allNS || *namespace == ""}
	report, err := analysis.GenerateReport(ctx, st, scope, version.Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "diagnose: %v\n", err)
		return 1
	}

	var data []byte
	if *format == "html" {
		data, err = analysis.RenderHTML(ctx, report)
	} else {
		data, err = json.MarshalIndent(report, "", "  ")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "diagnose: render: %v\n", err)
		return 1
	}

	out := io.Writer(os.Stdout)
	if *output != "" {
		f, err := os.Create(*output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "diagnose: %v\n", err)
			return 1
		}
		defer func() { _ = f.Close() }()
		out = f
	}
	if _, err := out.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "diagnose: %v\n", err)
		return 1
	}
	return 0
}

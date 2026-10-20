// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lithastra/kubeatlas/pkg/historyclient"
)

type historyClusterFlag struct {
	value string
	set   bool
}

func (f *historyClusterFlag) String() string { return f.value }
func (f *historyClusterFlag) Set(value string) error {
	if f.set || value == "" {
		return errors.New("--cluster must name exactly one member and occur once")
	}
	f.value = value
	f.set = true
	return nil
}

func runSnapshotRead(ctx context.Context, args []string, diff bool, out, diagnostics io.Writer) int {
	name := "snapshot list"
	if diff {
		name = "snapshot diff"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	fallback := os.Getenv("KUBEATLAS_URL")
	if fallback == "" {
		fallback = "http://127.0.0.1:8080"
	}
	server := fs.String("server", fallback, "Existing API base URL: HTTPS, or literal loopback HTTP. Defaults to KUBEATLAS_URL then http://127.0.0.1:8080.")
	var cluster historyClusterFlag
	fs.Var(&cluster, "cluster", "One attached member (required for federation); omit for standalone. Never inferred from kubeconfig.")
	timeout := fs.Duration("timeout", 10*time.Second, "Overall read timeout (1s..1m). One GET, no redirects, retries or API fallback. Optional bearer token: KUBEATLAS_TOKEN.")
	q := historyclient.Query{Diff: diff}
	if diff {
		fs.StringVar(&q.From, "from", "", "Required window start: duration, now or RFC3339.")
		fs.StringVar(&q.To, "to", "now", "Window end.")
		fs.StringVar(&q.Namespace, "namespace", "", "Optional namespace within the selected cluster.")
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}
	q.ClusterID = cluster.value
	if fs.NArg() != 0 || *timeout < time.Second || *timeout > time.Minute {
		fmt.Fprintln(diagnostics, name+": invalid positional arguments or timeout (1s..1m)")
		return 1
	}
	bounded, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	data, err := historyclient.Fetch(bounded, *server, os.Getenv("KUBEATLAS_TOKEN"), q)
	if err != nil {
		fmt.Fprintf(diagnostics, "%s: %v\n", name, err)
		return 1
	}
	if _, err := out.Write(data); err != nil {
		fmt.Fprintln(diagnostics, name+": cannot write history metadata")
		return 1
	}
	return 0
}

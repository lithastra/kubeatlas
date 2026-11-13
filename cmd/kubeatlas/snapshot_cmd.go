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

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/historyclient"
)

// runSnapshot is the `kubeatlas snapshot` subcommand group. Today
// it supports read-only list/diff and the separate internal trigger write.
func runSnapshot(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "snapshot: missing subcommand (expected: list, diff, trigger)")
		return 1
	}
	switch args[0] {
	case "list":
		return runSnapshotRead(context.Background(), args[1:], false, os.Stdout, os.Stderr)
	case "diff":
		return runSnapshotRead(context.Background(), args[1:], true, os.Stdout, os.Stderr)
	case "trigger":
		return runSnapshotTrigger(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "snapshot: unknown subcommand %q (expected: list, diff, trigger)\n", args[0])
		return 1
	}
}

// runSnapshotTrigger records one stored-graph observation through the internal
// operator endpoint. Exit 0: validated receipt; 1: usage, request, receipt or
// output failure; 2: non-200 response. Every invocation makes at most one POST.
func runSnapshotTrigger(args []string) int {
	return runSnapshotTriggerCommand(context.Background(), args, os.Stdout, os.Stderr)
}

// singleSnapshotFlag rejects repeated flags before dispatching a mutation.
type singleSnapshotFlag struct {
	flag.Value
	set bool
}

func (v *singleSnapshotFlag) String() string {
	if v == nil || v.Value == nil {
		return ""
	}
	return v.Value.String()
}

func (v *singleSnapshotFlag) Set(raw string) error {
	if v.set {
		return errors.New("flag must occur exactly once")
	}
	if err := v.Value.Set(raw); err != nil {
		return err
	}
	v.set = true
	return nil
}

func runSnapshotTriggerCommand(ctx context.Context, args []string, out, diagnostics io.Writer) int {
	fs := flag.NewFlagSet("snapshot trigger", flag.ContinueOnError)
	// Flag parsing errors can contain raw argument values. Keep inherited or
	// explicitly supplied credential-bearing URLs out of diagnostic output.
	fs.SetOutput(io.Discard)
	// Resolve the environment only after parsing, so --help cannot print a
	// credential-bearing URL inherited from KUBEATLAS_URL as a flag default.
	server := fs.String("server", "", "Existing internal operator URL: HTTPS, literal loopback HTTP or service.namespace.svc HTTP. Defaults to KUBEATLAS_URL then http://127.0.0.1:8080.")
	kind := fs.String("trigger", "manual", "One marker kind: manual or periodic. No retry or redirect; an error may have an unknown write outcome.")
	var cluster historyClusterFlag
	fs.Var(&cluster, "cluster", "One attached member (required for federation); omit for standalone. No fan-out or kubeconfig inference.")
	timeout := fs.Duration("timeout", historyclient.TriggerTimeout, "Overall deadline (1s..30s). Optional bearer credential comes only from KUBEATLAS_TOKEN.")
	fs.VisitAll(func(f *flag.Flag) { f.Value = &singleSnapshotFlag{Value: f.Value} })
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(diagnostics)
			_, _ = fmt.Fprintln(diagnostics, "Usage: kubeatlas snapshot trigger [flags]")
			fs.PrintDefaults()
		} else {
			_, _ = fmt.Fprintln(diagnostics, "snapshot trigger: invalid flags; use --help")
		}
		return 1
	}
	serverProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "server" {
			serverProvided = true
		}
	})
	if !serverProvided {
		*server = defaultSnapshotServer()
	}
	if fs.NArg() != 0 || *timeout < time.Second || *timeout > historyclient.TriggerTimeout {
		_, _ = fmt.Fprintln(diagnostics, "snapshot trigger: invalid positional arguments or timeout (1s..30s)")
		return 1
	}
	bounded, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	data, err := historyclient.Trigger(bounded, *server, os.Getenv("KUBEATLAS_TOKEN"), historyclient.TriggerQuery{ClusterID: cluster.value, Trigger: graph.SnapshotTrigger(*kind)})
	if err != nil {
		_, _ = fmt.Fprintf(diagnostics, "snapshot trigger: %v\n", err)
		var status *historyclient.TriggerStatusError
		if errors.As(err, &status) {
			return 2
		}
		return 1
	}
	if _, err := out.Write(data); err != nil {
		_, _ = fmt.Fprintln(diagnostics, "snapshot trigger: cannot write receipt; marker outcome may be unknown; no automatic retry")
		return 1
	}
	return 0
}

// defaultSnapshotServer resolves the API base URL: $KUBEATLAS_URL
// if set (the Helm CronJob points it at the ClusterIP Service),
// else literal loopback for an operator running the command beside a
// port-forward.
func defaultSnapshotServer() string {
	if v := os.Getenv("KUBEATLAS_URL"); v != "" {
		return v
	}
	return "http://127.0.0.1:8080"
}

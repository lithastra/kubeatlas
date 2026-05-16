// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// runSnapshot is the `kubeatlas snapshot` subcommand group. Today
// it has one sub-subcommand, `trigger`; the dispatch shape leaves
// room for future ones (e.g. `snapshot list`).
func runSnapshot(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "snapshot: missing subcommand (expected: trigger)")
		return 1
	}
	switch args[0] {
	case "trigger":
		return runSnapshotTrigger(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "snapshot: unknown subcommand %q (expected: trigger)\n", args[0])
		return 1
	}
}

// runSnapshotTrigger is `kubeatlas snapshot trigger`: POST the
// running KubeAtlas server's internal snapshot endpoint so it
// records a full-sync marker. The F-111 CronJob invokes this; an
// operator can also run it by hand.
//
// It deliberately does NOT touch the database directly — the
// server owns the snapshot_meta write (consistent permissions +
// one code path). The CLI is a thin HTTP client.
//
// Exit codes: 0 success, 1 usage / request error, 2 server returned
// a non-200.
func runSnapshotTrigger(args []string) int {
	fs := flag.NewFlagSet("snapshot trigger", flag.ContinueOnError)
	server := fs.String("server", defaultSnapshotServer(),
		"KubeAtlas API base URL. Defaults to $KUBEATLAS_URL, "+
			"else http://localhost:8080.")
	trigger := fs.String("trigger", "manual",
		"Marker kind recorded in snapshot_meta: 'periodic' "+
			"(CronJob) or 'manual' (operator).")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *trigger != "periodic" && *trigger != "manual" {
		fmt.Fprintf(os.Stderr, "snapshot trigger: --trigger must be 'periodic' or 'manual' (got %q)\n", *trigger)
		return 1
	}

	endpoint := strings.TrimRight(*server, "/") +
		"/api/_internal/snapshot/trigger?trigger=" + url.QueryEscape(*trigger)

	// A bounded timeout — the CronJob pod must not hang if the
	// server is unreachable; let Kubernetes retry the Job instead.
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(endpoint, "application/json", http.NoBody)
	if err != nil {
		fmt.Fprintf(os.Stderr, "snapshot trigger: POST %s: %v\n", endpoint, err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "snapshot trigger: server returned %d: %s\n",
			resp.StatusCode, strings.TrimSpace(string(body)))
		return 2
	}
	// Echo the server's JSON response so the CronJob log shows the
	// cluster size recorded at trigger time.
	fmt.Println(strings.TrimSpace(string(body)))
	return 0
}

// defaultSnapshotServer resolves the API base URL: $KUBEATLAS_URL
// if set (the Helm CronJob points it at the ClusterIP Service),
// else localhost for an operator running the command beside a
// port-forward.
func defaultSnapshotServer() string {
	if v := os.Getenv("KUBEATLAS_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

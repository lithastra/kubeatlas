// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
	"github.com/lithastra/kubeatlas/pkg/impact"
)

func newImpactCmd(a *app) *cobra.Command {
	var cluster, relation, uid, format, out string
	var depth, limit int
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "impact <Kind> <name>",
		Short: "Read one server-backed impact analysis; optionally export JSON or self-contained HTML",
		Long: "Read one captured v1 impact analysis from a KubeAtlas server (v1.7+).\n" +
			"Use the exact case-sensitive Kubernetes Kind, not kubectl aliases.\n" +
			"Namespace identifies the root, not a traversal filter; omit -n for cluster-scoped roots.\n" +
			"Federated servers require --cluster. No local analysis or legacy API fallback.\n\n" +
			"Server: --server, then KUBEATLAS_URL, then temporary kubectl port-forward.\n" +
			"Authentication: optional KUBEATLAS_TOKEN bearer token (never a command-line flag).\n" +
			"Remote servers require HTTPS; HTTP accepts literal loopback addresses only.\n" +
			"The command makes one request, never opens a browser, and closes its tunnel on exit.\n" +
			"Partial/truncated analyses are successful captures, not safety approvals.\n\n" +
			"Results go to stdout; diagnostics go to stderr. --out creates a new private file,\n" +
			"never overwrites. Exports contain sensitive cluster topology; review before sharing.",
		Example: "  kubectl atlas impact ConfigMap settings -n demo --server https://atlas.example\n" +
			"  kubectl atlas impact ServiceAccount api -n demo --cluster east --output json\n" +
			"  kubectl atlas impact Deployment api -n demo --expected-uid <uid> --output html --out impact.html",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := impact.Query{Namespace: a.resourceNamespace, Kind: args[0], Name: args[1], ClusterID: cluster,
				ExpectedUID: uid, Relation: analysis.ImpactRelation(relation), MaxDepth: depth, Limit: limit}
			if err := q.Validate(); err != nil {
				return err
			}
			if format != "text" && format != "json" && format != "html" {
				return errors.New("--output must be text, json, or html")
			}
			if timeout < time.Second || timeout > 2*time.Minute {
				return errors.New("--timeout must be between 1s and 2m")
			}
			if a.localUI || cmd.Flags().Changed("host") {
				return errors.New("impact reads an existing server; --local-ui and --host do not apply")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			base, cleanup, _, err := a.resolve(ctx, a.server, a.kubeatlasNamespace, a.kube())
			if err != nil {
				return errors.New("cannot resolve impact server; check --server, KUBEATLAS_URL, or kubectl port-forward access")
			}
			defer cleanup()
			response, err := impact.Fetch(ctx, base, os.Getenv("KUBEATLAS_TOKEN"), q)
			if err != nil {
				return err
			}
			data, err := impact.Render(response, format)
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			// O_EXCL also rejects existing symlinks. Keep a partial file on I/O
			// failure rather than deleting a path another process could replace.
			file, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return fmt.Errorf("cannot create report (no overwrite): %w", err)
			}
			_, writeErr := file.Write(data)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				return errors.New("report write failed; a partial file may remain at --out")
			}
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "Wrote impact report to %q (sensitive cluster topology).\n", out)
			return err
		},
	}
	cmd.Flags().StringVar(&cluster, "cluster", "", "Exact member cluster ID (required for federated servers)")
	cmd.Flags().StringVar(&relation, "relation", string(analysis.ImpactDependents), "Relationship traversal: dependents or dependencies")
	cmd.Flags().IntVar(&depth, "max-depth", analysis.DefaultImpactDepth, "Traversal depth (1..10)")
	cmd.Flags().IntVar(&limit, "limit", analysis.DefaultImpactLimit, "Maximum results per facet (1..1000)")
	cmd.Flags().StringVar(&uid, "expected-uid", "", "Require this root instance UID; absent means the current named instance")
	cmd.Flags().StringVarP(&format, "output", "o", "text", "Output format: text, json, or html")
	cmd.Flags().StringVar(&out, "out", "", "Create a new report file with mode 0600 (default or -: stdout)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Deadline including discovery (1s..2m)")
	return cmd
}

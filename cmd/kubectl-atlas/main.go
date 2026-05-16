// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Command kubectl-atlas is the KubeAtlas kubectl plugin (F-116).
// Installed on PATH as `kubectl-atlas`, kubectl exposes it as
// `kubectl atlas`. Its single job is to open the KubeAtlas UI at the
// page for a resource, namespace, or the whole cluster — it is an
// entry point, not a second CLI. Everything else KubeAtlas can do is
// the `kubeatlas` binary's job.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// app holds the plugin's runtime wiring. open and resolve are fields,
// not direct calls, so the command tests can inject a fake browser
// opener and a fake server resolver — CI has neither a display nor a
// cluster.
type app struct {
	server             string // --server
	kubeatlasNamespace string // --kubeatlas-namespace
	resourceNamespace  string // -n / --namespace (the resource's namespace)

	open    opener
	resolve func(ctx context.Context, flagValue, kubeatlasNamespace string) (string, func(), bool, error)
}

func main() {
	a := &app{open: systemBrowser, resolve: resolveServer}
	if err := newRootCmd(a).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "kubectl-atlas:", err)
		os.Exit(1)
	}
}

// newRootCmd builds the command tree. The root itself handles the
// `<kind> <name>` resource form; `namespace` and `cluster` are
// subcommands. Cobra routes a first arg that matches a subcommand
// name to that subcommand and everything else to the root's RunE.
func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "atlas <kind> <name>",
		Short: "Open the KubeAtlas UI for a Kubernetes resource",
		Long: "kubectl-atlas opens the KubeAtlas UI at the page for a resource,\n" +
			"namespace, or the whole cluster.\n\n" +
			"The UI base URL is resolved in order: --server, then the\n" +
			"KUBEATLAS_URL environment variable, then a kubectl port-forward\n" +
			"to the in-cluster KubeAtlas Service.",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.openTarget(cmd.Context(), func(base string) string {
				return resourceURL(base, a.resourceNamespace, args[0], args[1])
			})
		},
	}
	root.PersistentFlags().StringVar(&a.server, "server", "",
		"KubeAtlas UI base URL (overrides KUBEATLAS_URL and auto-discovery)")
	root.PersistentFlags().StringVar(&a.kubeatlasNamespace, "kubeatlas-namespace",
		defaultKubeatlasNamespace, "Namespace KubeAtlas is installed in (for port-forward discovery)")
	root.PersistentFlags().StringVarP(&a.resourceNamespace, "namespace", "n", "",
		"Namespace of the resource to open")
	root.AddCommand(newNamespaceCmd(a), newClusterCmd(a))
	return root
}

func newNamespaceCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:           "namespace <name>",
		Short:         "Open the KubeAtlas topology view for a namespace",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.openTarget(cmd.Context(), func(base string) string {
				return namespaceURL(base, args[0])
			})
		},
	}
}

func newClusterCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:           "cluster",
		Short:         "Open the KubeAtlas cluster topology view",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.openTarget(cmd.Context(), clusterURL)
		},
	}
}

// openTarget resolves the server, builds the target URL with build,
// and opens it. When discovery established a port-forward tunnel the
// call blocks until the operator interrupts it — otherwise the tunnel
// (and the page) would die the instant the plugin returned.
func (a *app) openTarget(ctx context.Context, build func(base string) string) error {
	base, cleanup, tunnel, err := a.resolve(ctx, a.server, a.kubeatlasNamespace)
	if err != nil {
		return err
	}
	defer cleanup()

	target := build(base)
	fmt.Fprintln(os.Stdout, "Opening", target)
	if err := a.open(target); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}

	if tunnel {
		fmt.Fprintln(os.Stdout, "Port-forward tunnel is up — press Ctrl-C to close it.")
		waitForInterrupt(ctx)
	}
	return nil
}

// waitForInterrupt blocks until SIGINT/SIGTERM or ctx cancellation.
func waitForInterrupt(ctx context.Context) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	select {
	case <-sig:
	case <-ctx.Done():
	}
}

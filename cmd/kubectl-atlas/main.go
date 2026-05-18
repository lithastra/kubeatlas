// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Command kubectl-atlas is the KubeAtlas kubectl plugin. Installed on
// PATH as `kubectl-atlas`, kubectl exposes it as `kubectl atlas`. It
// shows a KubeAtlas view of a resource, namespace, or the whole
// cluster in one of two modes:
//
//   - offline (the default) — builds the dependency graph itself
//     from the Kubernetes API and renders it to a local SVG; no
//     KubeAtlas server is needed.
//   - online — opens the live KubeAtlas web UI; selected with
//     --online, --server, or the KUBEATLAS_URL environment variable.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
)

// app holds the plugin's runtime wiring. open, resolve and render
// are fields, not direct calls, so the command tests can inject
// fakes — CI has neither a display, a cluster, nor the kubeatlas
// binary.
type app struct {
	server             string // --server
	kubeatlasNamespace string // --kubeatlas-namespace
	resourceNamespace  string // -n / --namespace (the resource's namespace)
	online             bool   // --online
	kubeContext        string // --context
	kubeconfig         string // --kubeconfig

	open    opener
	resolve func(ctx context.Context, flagValue, kubeatlasNamespace string, kf kubeFlags) (string, func(), bool, error)
	render  func(ctx context.Context, namespace string, kf kubeFlags) ([]byte, error)
}

// kubeFlags carries the kubectl-style cluster-selection flags the
// plugin passes through to the kubectl / kubeatlas subprocesses it
// shells out to. Both are empty unless the operator sets --context
// or --kubeconfig.
type kubeFlags struct {
	context    string
	kubeconfig string
}

// kube bundles the plugin's cluster-selection flags.
func (a *app) kube() kubeFlags {
	return kubeFlags{context: a.kubeContext, kubeconfig: a.kubeconfig}
}

// kubectlArgs renders the flags as kubectl command-line arguments.
func (k kubeFlags) kubectlArgs() []string {
	var args []string
	if k.context != "" {
		args = append(args, "--context", k.context)
	}
	if k.kubeconfig != "" {
		args = append(args, "--kubeconfig", k.kubeconfig)
	}
	return args
}

func main() {
	a := &app{open: systemBrowser, resolve: resolveServer, render: renderOffline}
	if err := newRootCmd(a).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "kubectl-atlas:", err)
		os.Exit(1)
	}
}

// target is what one invocation points at. namespace is the scope —
// an empty namespace means the whole cluster. onlineURL builds the
// UI deep-link used in online mode; offline mode renders the
// namespace (or cluster) graph.
type target struct {
	namespace string
	onlineURL func(base string) string
}

// newRootCmd builds the command tree. The root handles the
// `<kind> <name>` resource form; `namespace` and `cluster` are
// subcommands. Cobra routes a first arg that matches a subcommand
// name to that subcommand and everything else to the root's RunE.
func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		// Use begins with the binary name so help and usage strings
		// carry the kubectl-plugin prefix (Krew best practice) — e.g.
		// "Usage: kubectl-atlas namespace <name>" rather than a bare
		// "atlas". kubectl invokes the plugin as `kubectl atlas`.
		Use:   "kubectl-atlas <kind> <name>",
		Short: "Show a KubeAtlas view of a Kubernetes resource",
		Long: "kubectl-atlas shows a KubeAtlas view of a resource, namespace,\n" +
			"or the whole cluster.\n\n" +
			"Offline (the default): builds the dependency graph straight from\n" +
			"the Kubernetes API and renders it to an SVG file — no KubeAtlas\n" +
			"server required.\n\n" +
			"Online (--online, --server, or KUBEATLAS_URL): opens the live\n" +
			"KubeAtlas web UI. The server URL is resolved from --server, then\n" +
			"KUBEATLAS_URL, then a kubectl port-forward to the in-cluster\n" +
			"KubeAtlas Service.",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd.Context(), target{
				namespace: a.resourceNamespace,
				onlineURL: func(base string) string {
					return resourceURL(base, a.resourceNamespace, args[0], args[1])
				},
			})
		},
	}
	root.PersistentFlags().StringVar(&a.server, "server", "",
		"KubeAtlas UI base URL — implies online mode (overrides KUBEATLAS_URL and auto-discovery)")
	root.PersistentFlags().BoolVar(&a.online, "online", false,
		"Use a running KubeAtlas server and open the live UI instead of rendering offline")
	root.PersistentFlags().StringVar(&a.kubeatlasNamespace, "kubeatlas-namespace",
		defaultKubeatlasNamespace, "Namespace KubeAtlas is installed in (for online port-forward discovery)")
	root.PersistentFlags().StringVarP(&a.resourceNamespace, "namespace", "n", "",
		"Namespace of the resource")
	root.PersistentFlags().StringVar(&a.kubeContext, "context", "",
		"kubeconfig context to target — passed through to kubectl and kubeatlas")
	root.PersistentFlags().StringVar(&a.kubeconfig, "kubeconfig", "",
		"Path to the kubeconfig file — passed through to kubectl and kubeatlas")
	root.AddCommand(newNamespaceCmd(a), newClusterCmd(a))
	return root
}

func newNamespaceCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:           "namespace <name>",
		Short:         "Show the KubeAtlas topology for a namespace",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd.Context(), target{
				namespace: args[0],
				onlineURL: func(base string) string { return namespaceURL(base, args[0]) },
			})
		},
	}
}

func newClusterCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:           "cluster",
		Short:         "Show the KubeAtlas cluster topology",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd.Context(), target{namespace: "", onlineURL: clusterURL})
		},
	}
}

// useOnline reports whether to use a running KubeAtlas server. The
// default is offline; online is selected by --online, --server, or
// the KUBEATLAS_URL environment variable.
func (a *app) useOnline() bool {
	return a.online || a.server != "" || os.Getenv("KUBEATLAS_URL") != ""
}

// run dispatches t to the online or offline path.
func (a *app) run(ctx context.Context, t target) error {
	if a.useOnline() {
		return a.runOnline(ctx, t)
	}
	return a.runOffline(ctx, t)
}

// runOnline resolves the KubeAtlas server, opens the UI deep-link,
// and — when discovery established a port-forward tunnel — blocks
// until the operator interrupts it, otherwise the tunnel (and the
// page) would die the instant the plugin returned.
func (a *app) runOnline(ctx context.Context, t target) error {
	base, cleanup, tunnel, err := a.resolve(ctx, a.server, a.kubeatlasNamespace, a.kube())
	if err != nil {
		return err
	}
	defer cleanup()

	dst := t.onlineURL(base)
	_, _ = fmt.Fprintln(os.Stdout, "Opening", dst)
	if err := a.open(dst); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	if tunnel {
		_, _ = fmt.Fprintln(os.Stdout, "Port-forward tunnel is up — press Ctrl-C to close it.")
		waitForInterrupt(ctx)
	}
	return nil
}

// runOffline renders the graph locally — no KubeAtlas server. It
// shells out to `kubeatlas -once -format=svg` through a.render,
// writes the SVG to kubeatlas-<scope>.svg in the working directory,
// and opens it.
func (a *app) runOffline(ctx context.Context, t target) error {
	svg, err := a.render(ctx, t.namespace, a.kube())
	if err != nil {
		return err
	}
	scope := t.namespace
	if scope == "" {
		scope = "cluster"
	}
	out := "kubeatlas-" + scope + ".svg"
	if err := os.WriteFile(out, svg, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		abs = out
	}
	_, _ = fmt.Fprintln(os.Stdout, "Rendered", abs)
	if err := a.open(abs); err != nil {
		return fmt.Errorf("open browser: %w", err)
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

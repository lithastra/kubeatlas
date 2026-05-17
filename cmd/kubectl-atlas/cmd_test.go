// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// newTestApp builds an app with fakes for the browser opener, the
// server resolver, and the offline renderer, so command tests need
// neither a display, a cluster, nor the kubeatlas binary.
func newTestApp() (a *app, opened *string) {
	var url string
	a = &app{
		open: func(u string) error { url = u; return nil },
		resolve: func(_ context.Context, _, _ string) (string, func(), bool, error) {
			return "http://atlas.example", noopCleanup, false, nil
		},
		render: func(_ context.Context, _ string) ([]byte, error) {
			return []byte("<svg>fake</svg>"), nil
		},
	}
	return a, &url
}

func runCmd(t *testing.T, a *app, args ...string) error {
	t.Helper()
	cmd := newRootCmd(a)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.Execute()
}

// --- online mode (--online) -----------------------------------------

func TestRoot_OnlineOpensResourceURL(t *testing.T) {
	a, opened := newTestApp()
	if err := runCmd(t, a, "--online", "Deployment", "api", "-n", "petclinic"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if want := "http://atlas.example/resources/petclinic/Deployment/api"; *opened != want {
		t.Errorf("opened %q, want %q", *opened, want)
	}
}

func TestRoot_OnlineNoNamespaceUsesClusterSentinel(t *testing.T) {
	a, opened := newTestApp()
	if err := runCmd(t, a, "--online", "Node", "worker-1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if want := "http://atlas.example/resources/_/Node/worker-1"; *opened != want {
		t.Errorf("opened %q, want %q", *opened, want)
	}
}

func TestRoot_RequiresTwoArgs(t *testing.T) {
	a, opened := newTestApp()
	if err := runCmd(t, a, "Deployment"); err == nil {
		t.Error("expected an error for a single positional arg")
	}
	if *opened != "" {
		t.Errorf("browser opened %q on an invalid invocation", *opened)
	}
}

func TestNamespaceSubcommand_OnlineOpensNamespaceURL(t *testing.T) {
	a, opened := newTestApp()
	if err := runCmd(t, a, "--online", "namespace", "petclinic"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if want := "http://atlas.example/topology?level=namespace&namespace=petclinic"; *opened != want {
		t.Errorf("opened %q, want %q", *opened, want)
	}
}

func TestClusterSubcommand_OnlineOpensClusterURL(t *testing.T) {
	a, opened := newTestApp()
	if err := runCmd(t, a, "--online", "cluster"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if want := "http://atlas.example/topology?level=cluster"; *opened != want {
		t.Errorf("opened %q, want %q", *opened, want)
	}
}

// --server selects online mode without an explicit --online.
func TestServerFlag_ImpliesOnline(t *testing.T) {
	a, opened := newTestApp()
	if err := runCmd(t, a, "--server", "http://given.example", "cluster"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if *opened == "" {
		t.Error("--server should select online mode and open a URL")
	}
}

func TestExecute_OnlinePropagatesResolveError(t *testing.T) {
	a, opened := newTestApp()
	a.resolve = func(_ context.Context, _, _ string) (string, func(), bool, error) {
		return "", noopCleanup, false, errors.New("server not found")
	}
	if err := runCmd(t, a, "--online", "Deployment", "api"); err == nil {
		t.Error("expected the resolve error to surface from Execute")
	}
	if *opened != "" {
		t.Errorf("browser opened %q despite a discovery failure", *opened)
	}
}

func TestExecute_OnlinePropagatesOpenError(t *testing.T) {
	a, _ := newTestApp()
	a.open = func(string) error { return errors.New("no browser") }
	if err := runCmd(t, a, "--online", "cluster"); err == nil {
		t.Error("expected the browser-open error to surface from Execute")
	}
}

// --- offline mode (the default) -------------------------------------

func TestCluster_OfflineRendersAndOpensFile(t *testing.T) {
	t.Chdir(t.TempDir())
	a, opened := newTestApp()
	if err := runCmd(t, a, "cluster"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	const out = "kubeatlas-cluster.svg"
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("offline render wrote no %s: %v", out, err)
	}
	if string(data) != "<svg>fake</svg>" {
		t.Errorf("file contents = %q", data)
	}
	abs, _ := filepath.Abs(out)
	if *opened != abs {
		t.Errorf("opened %q, want the rendered file %q", *opened, abs)
	}
}

func TestNamespace_OfflinePassesNamespaceAndNamesFile(t *testing.T) {
	t.Chdir(t.TempDir())
	a, _ := newTestApp()
	var gotNS string
	a.render = func(_ context.Context, ns string) ([]byte, error) {
		gotNS = ns
		return []byte("<svg/>"), nil
	}
	if err := runCmd(t, a, "namespace", "petclinic"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotNS != "petclinic" {
		t.Errorf("render got namespace %q, want petclinic", gotNS)
	}
	if _, err := os.Stat("kubeatlas-petclinic.svg"); err != nil {
		t.Errorf("expected kubeatlas-petclinic.svg: %v", err)
	}
}

func TestResource_OfflineRendersResourceNamespace(t *testing.T) {
	t.Chdir(t.TempDir())
	a, _ := newTestApp()
	var gotNS string
	a.render = func(_ context.Context, ns string) ([]byte, error) {
		gotNS = ns
		return []byte("<svg/>"), nil
	}
	if err := runCmd(t, a, "Deployment", "api", "-n", "petclinic"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotNS != "petclinic" {
		t.Errorf("render got namespace %q, want the resource namespace petclinic", gotNS)
	}
}

func TestOffline_PropagatesRenderError(t *testing.T) {
	t.Chdir(t.TempDir())
	a, opened := newTestApp()
	a.render = func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("kubeatlas not found")
	}
	if err := runCmd(t, a, "cluster"); err == nil {
		t.Error("expected the render error to surface from Execute")
	}
	if *opened != "" {
		t.Errorf("opened %q despite a render failure", *opened)
	}
}

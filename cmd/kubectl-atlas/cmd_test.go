// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io"
	"testing"
)

// newTestApp builds an app with a fake browser opener (records the
// URL) and a fake resolver (a fixed base, no tunnel) so command tests
// need neither a display nor a cluster.
func newTestApp() (a *app, opened *string) {
	var url string
	a = &app{
		open: func(u string) error { url = u; return nil },
		resolve: func(_ context.Context, _, _ string) (string, func(), bool, error) {
			return "http://atlas.example", noopCleanup, false, nil
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

func TestRoot_OpensResourceURL(t *testing.T) {
	a, opened := newTestApp()
	if err := runCmd(t, a, "Deployment", "api", "-n", "petclinic"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if want := "http://atlas.example/resources/petclinic/Deployment/api"; *opened != want {
		t.Errorf("opened %q, want %q", *opened, want)
	}
}

func TestRoot_NoNamespaceUsesClusterSentinel(t *testing.T) {
	a, opened := newTestApp()
	if err := runCmd(t, a, "Node", "worker-1"); err != nil {
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

func TestNamespaceSubcommand_OpensNamespaceURL(t *testing.T) {
	a, opened := newTestApp()
	if err := runCmd(t, a, "namespace", "petclinic"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if want := "http://atlas.example/topology?level=namespace&namespace=petclinic"; *opened != want {
		t.Errorf("opened %q, want %q", *opened, want)
	}
}

func TestClusterSubcommand_OpensClusterURL(t *testing.T) {
	a, opened := newTestApp()
	if err := runCmd(t, a, "cluster"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if want := "http://atlas.example/topology?level=cluster"; *opened != want {
		t.Errorf("opened %q, want %q", *opened, want)
	}
}

func TestExecute_PropagatesResolveError(t *testing.T) {
	a, opened := newTestApp()
	a.resolve = func(_ context.Context, _, _ string) (string, func(), bool, error) {
		return "", noopCleanup, false, errors.New("server not found")
	}
	if err := runCmd(t, a, "Deployment", "api"); err == nil {
		t.Error("expected the resolve error to surface from Execute")
	}
	if *opened != "" {
		t.Errorf("browser opened %q despite a discovery failure", *opened)
	}
}

func TestExecute_PropagatesOpenError(t *testing.T) {
	a, _ := newTestApp()
	a.open = func(string) error { return errors.New("no browser") }
	if err := runCmd(t, a, "cluster"); err == nil {
		t.Error("expected the browser-open error to surface from Execute")
	}
}

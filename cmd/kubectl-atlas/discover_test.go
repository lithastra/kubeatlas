// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
)

// resolveServer priority 3 (kubectl port-forward) shells out to a
// real cluster, so only priorities 1 (--server) and 2 (KUBEATLAS_URL)
// are unit-tested here.

func TestResolveServer_FlagWins(t *testing.T) {
	// A trailing slash on --server must be normalised away.
	base, _, tunnel, err := resolveServer(context.Background(), "http://flag.example/", "kubeatlas")
	if err != nil {
		t.Fatalf("resolveServer: %v", err)
	}
	if base != "http://flag.example" {
		t.Errorf("base = %q, want http://flag.example", base)
	}
	if tunnel {
		t.Error("tunnel = true, want false for the --server path")
	}
}

func TestResolveServer_EnvUsedWhenNoFlag(t *testing.T) {
	t.Setenv("KUBEATLAS_URL", "http://env.example")
	base, _, tunnel, err := resolveServer(context.Background(), "", "kubeatlas")
	if err != nil {
		t.Fatalf("resolveServer: %v", err)
	}
	if base != "http://env.example" {
		t.Errorf("base = %q, want http://env.example", base)
	}
	if tunnel {
		t.Error("tunnel = true, want false for the KUBEATLAS_URL path")
	}
}

func TestResolveServer_FlagBeatsEnv(t *testing.T) {
	t.Setenv("KUBEATLAS_URL", "http://env.example")
	base, _, _, err := resolveServer(context.Background(), "http://flag.example", "kubeatlas")
	if err != nil {
		t.Fatalf("resolveServer: %v", err)
	}
	if base != "http://flag.example" {
		t.Errorf("base = %q, want the --server value to beat KUBEATLAS_URL", base)
	}
}

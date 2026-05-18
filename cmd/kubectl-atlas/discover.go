// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// defaultKubeatlasNamespace is the namespace `helm install kubeatlas`
// lands in by default; the port-forward fallback targets the
// same-named Service there.
const (
	defaultKubeatlasNamespace = "kubeatlas"
	kubeatlasServiceName      = "kubeatlas"
	kubeatlasServicePort      = 8080
)

// noopCleanup is the cleanup returned by the flag / env discovery
// paths — they hold no resources.
func noopCleanup() {}

// resolveServer works out the KubeAtlas UI base URL in priority
// order:
//
//  1. an explicit --server value
//  2. the KUBEATLAS_URL environment variable
//  3. a `kubectl port-forward` tunnel to the in-cluster Service
//
// For 1 and 2 the cleanup is a no-op and tunnel is false. For 3 the
// cleanup tears the port-forward down, and tunnel is true so the
// caller knows to hold the process open while the browser tab lives.
func resolveServer(ctx context.Context, flagValue, kubeatlasNamespace string, kf kubeFlags) (base string, cleanup func(), tunnel bool, err error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return normalizeBase(v), noopCleanup, false, nil
	}
	if v := strings.TrimSpace(os.Getenv("KUBEATLAS_URL")); v != "" {
		return normalizeBase(v), noopCleanup, false, nil
	}

	base, cleanup, err = startPortForward(ctx, kubeatlasNamespace, kf)
	if err != nil {
		return "", noopCleanup, false, fmt.Errorf(
			"KubeAtlas server not found — pass --server, set KUBEATLAS_URL, "+
				"or install KubeAtlas in the %q namespace "+
				"(https://docs.kubeatlas.lithastra.com): %w",
			kubeatlasNamespace, err)
	}
	return base, cleanup, true, nil
}

// forwardingLine matches the address kubectl prints once a
// port-forward is established: "Forwarding from 127.0.0.1:54xxx -> 8080".
var forwardingLine = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+)`)

// startPortForward shells out to `kubectl port-forward` against the
// KubeAtlas Service, picking a random local port, and returns the
// resulting base URL plus a cleanup that kills the tunnel.
//
// The plugin embeds no Kubernetes client — it reuses the operator's
// kubectl, inheriting their auth — and threads --context /
// --kubeconfig through so the tunnel targets the same cluster the
// rest of the invocation does.
func startPortForward(ctx context.Context, namespace string, kf kubeFlags) (string, func(), error) {
	args := []string{"port-forward", "-n", namespace}
	args = append(args, kf.kubectlArgs()...)
	args = append(args, "svc/"+kubeatlasServiceName, fmt.Sprintf(":%d", kubeatlasServicePort))
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", noopCleanup, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", noopCleanup, fmt.Errorf("kubectl port-forward: %w (is kubectl on PATH?)", err)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	// kubectl prints the Forwarding line once the tunnel is live.
	// Scan stdout for it; if the stream closes first the tunnel never
	// came up (no Service, wrong namespace, RBAC).
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if m := forwardingLine.FindStringSubmatch(scanner.Text()); m != nil {
			return "http://127.0.0.1:" + m[1], cleanup, nil
		}
	}
	cleanup()
	return "", noopCleanup, fmt.Errorf(
		"kubectl port-forward reported no local port — is svc/%s running in namespace %q?",
		kubeatlasServiceName, namespace)
}

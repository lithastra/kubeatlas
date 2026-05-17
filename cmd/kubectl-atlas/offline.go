// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// renderOffline renders the resource graph without a KubeAtlas
// server by shelling out to `kubeatlas -once -format=svg`. namespace
// narrows the render; an empty namespace renders the whole cluster.
//
// `kubeatlas -once` is the offline CLI mode — it reads the cluster
// straight from the Kubernetes API through the kubeconfig — so the
// plugin reuses it rather than embedding any discovery code. The
// `kubeatlas` binary must be on PATH.
func renderOffline(ctx context.Context, namespace string) ([]byte, error) {
	bin, err := exec.LookPath("kubeatlas")
	if err != nil {
		return nil, fmt.Errorf("offline mode needs the `kubeatlas` binary on PATH — " +
			"install it, or pass --online (or --server) to use a running KubeAtlas server")
	}

	args := []string{"-once", "-format=svg"}
	if namespace != "" {
		args = append(args, "-namespace="+namespace)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	svg, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("kubeatlas -once failed: %w\n%s", err, stderr.String())
	}
	return svg, nil
}

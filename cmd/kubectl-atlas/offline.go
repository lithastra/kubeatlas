// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/lithastra/kubeatlas/pkg/collect"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// renderOffline builds the dependency graph in-process and renders
// it to SVG. It collects the cluster straight from the Kubernetes
// API through the kubeconfig (the shared collect.Cluster scan) — no
// KubeAtlas server and no separate kubeatlas binary are involved.
//
// namespace narrows the render; an empty namespace renders the
// whole cluster. The graphviz `dot` binary must be on PATH for the
// SVG step.
func renderOffline(ctx context.Context, namespace string, kf kubeFlags) ([]byte, error) {
	st := memory.New()
	if err := collect.Cluster(ctx, st, kf.kubeconfig, kf.context); err != nil {
		return nil, err
	}
	g, err := st.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}
	svg, err := graph.ToSVG(ctx, g, graph.DOTOptions{Namespace: namespace})
	if err != nil {
		if errors.Is(err, graph.ErrGraphvizNotFound) {
			return nil, fmt.Errorf("offline rendering needs the graphviz `dot` binary on PATH — " +
				"install graphviz, or pass --online (or --server) to use a running KubeAtlas server")
		}
		return nil, fmt.Errorf("render svg: %w", err)
	}
	return svg, nil
}

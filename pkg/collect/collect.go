// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package collect runs the offline one-shot cluster scan: it reads
// every resource the cluster exposes through the kubeconfig, derives
// the built-in edge types, and writes the result into a GraphStore.
//
// It is the shared core of `kubeatlas -once` and the kubectl-atlas
// plugin's offline mode — neither needs a running KubeAtlas server.
package collect

import (
	"context"
	"fmt"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Cluster collects every resource the cluster exposes — talking to
// the Kubernetes API directly through the kubeconfig, no KubeAtlas
// server involved — and writes each resource, plus the edges the
// built-in extractors derive, into st.
//
// kubeconfigPath and contextName select the cluster; both may be
// empty for the default kubeconfig and its current context. The
// pipeline is the same extractor.Default() the informer runs, so an
// offline scan produces the same edge types as the live graph.
func Cluster(ctx context.Context, st graph.GraphStore, kubeconfigPath, contextName string) error {
	client, err := discovery.NewClient(kubeconfigPath, contextName)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	resources, err := client.CollectAll()
	if err != nil {
		return fmt.Errorf("collect resources: %w", err)
	}

	for _, r := range resources {
		if err := st.UpsertResource(ctx, r); err != nil {
			return fmt.Errorf("upsert resource %s: %w", r.ID(), err)
		}
	}

	// Extract edges through the typed extractor.Registry — the same
	// path the informer uses, so an offline scan and the watch loop
	// produce the same edge types. Every resource is already in st,
	// so the selector extractors query it directly.
	reg := extractor.Default()
	for _, r := range resources {
		edges, err := reg.ExtractAll(ctx, r, st)
		if err != nil {
			return fmt.Errorf("extract edges for %s: %w", r.ID(), err)
		}
		for _, e := range edges {
			if err := st.UpsertEdge(ctx, e); err != nil {
				return fmt.Errorf("upsert edge %s -> %s: %w", e.From, e.To, err)
			}
		}
	}
	return nil
}

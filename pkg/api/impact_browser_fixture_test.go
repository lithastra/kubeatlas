// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/multicluster"
)

// Compiled into a test-only binary for the production-browser/actual-CLI suite.
// Normal Go tests skip this bounded opt-in runner. It loads no Kubernetes
// configuration, starts no informer/database, and can bind only literal loopback.
func TestImpactBrowserFixtureServer(t *testing.T) {
	if os.Getenv("KUBEATLAS_IMPACT_BROWSER_FIXTURE") != "1" {
		t.Skip("only run by the bounded live impact browser suite")
	}
	if _, err := os.Stat("../../web/dist/index.html"); err != nil {
		t.Fatalf("build the production Web bundle first: %v", err)
	}
	_, store := loadImpactClientMatrix(t)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	servers := []*Server{
		New("127.0.0.1:4174", store, aggregator.NewRegistry(), WithWebFS(os.DirFS("../.."))),
		New("127.0.0.1:4175", store, aggregator.NewRegistry(), WithWebFS(os.DirFS("../..")),
			WithClusterLister(&impactAPIClusters{clusters: []string{"east", "west"}}),
			WithClusterRBAC(multicluster.NewRBACScope([]multicluster.RBACRule{{Token: "synthetic-matrix-east", Clusters: []string{"east"}}}))),
	}
	results := make(chan error, len(servers))
	for _, server := range servers {
		go func() { results <- server.Start(ctx) }()
	}
	// One failure cancels its sibling; collect both results before returning.
	// Server.Start uses the production routes/middleware and drains its listener.
	for range servers {
		err := <-results
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("fixture server failed: %v", err)
		}
	}
}

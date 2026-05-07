// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// Shared across benchmark invocations so Go's b.N auto-scaling does
// not re-bootstrap a container every pass. TestMain owns the
// lifetime; sync.Once gates lazy creation so tests that never need
// the bench store do not pay for it.
var (
	benchOnce   sync.Once
	benchHandle AGEHandle
	benchStore  *Store
	benchInitOK bool
)

func TestMain(m *testing.M) {
	code := m.Run()
	if benchStore != nil {
		benchStore.Close()
	}
	if benchHandle.Container != nil {
		_ = benchHandle.Container.Terminate(context.Background())
	}
	os.Exit(code)
}

// sharedBenchStore lazily boots one container/store and reuses it
// across every benchmark scaling pass in this package. Bootstrap
// errors fail the calling benchmark; subsequent calls short-circuit.
func sharedBenchStore(b *testing.B) *Store {
	b.Helper()
	benchOnce.Do(func() {
		ctx := context.Background()
		h, err := bootstrapPostgresWithAGE(ctx)
		if err != nil {
			b.Logf("sharedBenchStore: bootstrap: %v", err)
			return
		}
		s, err := New(ctx, Config{DSN: h.ConnStr})
		if err != nil {
			_ = h.Container.Terminate(context.Background())
			b.Logf("sharedBenchStore: New: %v", err)
			return
		}
		benchHandle, benchStore, benchInitOK = h, s, true
	})
	if !benchInitOK {
		b.Fatal("shared bench store unavailable; check earlier log output")
	}
	return benchStore
}

// BenchmarkUpsert1000Resources is the P2-T2 acceptance benchmark:
// each iteration upserts 1000 unique resources into a freshly-
// truncated store, so ns/op reports the time to load a small
// cluster's worth of resources end-to-end (marshal -> SQL -> COMMIT).
//
// Guide budget: `go test -bench=BenchmarkUpsert1000Resources` under
// 5s wall on testcontainers PG. The shared TestMain container makes
// this hold even when Go auto-scales b.N across multiple passes.
func BenchmarkUpsert1000Resources(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping testcontainers benchmark in -short mode")
	}

	store := sharedBenchStore(b)
	ctx := context.Background()

	// Pre-build the resource fixtures so the timed loop measures the
	// upsert path itself, not Resource construction or fmt.Sprintf.
	resources := make([]graph.Resource, 1000)
	for i := range resources {
		resources[i] = graph.Resource{
			Kind:      "ConfigMap",
			Namespace: "bench",
			Name:      fmt.Sprintf("cm-%04d", i),
			Labels:    map[string]string{"app": "bench"},
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if err := store.truncateAll(ctx); err != nil {
			b.Fatalf("truncateAll: %v", err)
		}
		b.StartTimer()

		for _, r := range resources {
			if err := store.UpsertResource(ctx, r); err != nil {
				b.Fatalf("UpsertResource: %v", err)
			}
		}
	}
}

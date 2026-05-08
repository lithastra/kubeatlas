// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// BenchmarkEvaluateForResource_CacheWarm measures the warm-cache hot
// path: a single resource is evaluated once (miss + populate), then
// the timed loop hits the cache repeatedly. Target throughput from
// guide §1.7: > 5K eval/s on the OPA SDK baseline.
func BenchmarkEvaluateForResource_CacheWarm(b *testing.B) {
	pack, err := LoadRulePackFromDir(sampleDir)
	if err != nil {
		b.Fatalf("LoadRulePackFromDir: %v", err)
	}

	metrics := NewMetrics()
	cache, err := NewCache(1000, metrics)
	if err != nil {
		b.Fatal(err)
	}
	router := FromRulePacks(pack)
	e := New(WithRouter(router), WithCache(cache), WithMetrics(metrics))
	if err := pack.RegisterTo(context.Background(), e); err != nil {
		b.Fatalf("RegisterTo: %v", err)
	}

	r := graph.Resource{
		Kind:            "Foo",
		Name:            "bench",
		Namespace:       "demo",
		GroupVersion:    "example.com/v1",
		UID:             types.UID("uid-bench"),
		ResourceVersion: "1",
	}

	// Warm the cache once outside the timed loop.
	if _, err := e.EvaluateForResource(context.Background(), r); err != nil {
		b.Fatalf("warm: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.EvaluateForResource(context.Background(), r); err != nil {
			b.Fatalf("hot loop: %v", err)
		}
	}

	// Sanity check after the loop: no new misses, all hits.
	snap := metrics.Snapshot()
	if snap.CacheMisses != 1 {
		b.Errorf("warm-cache benchmark recorded %d misses, want exactly 1", snap.CacheMisses)
	}
}

// BenchmarkEvaluateForResource_ColdMiss exercises the OPA hot path
// (every iteration is a fresh resourceVersion → guaranteed miss).
// This sets the floor for cold-cache throughput; the warm benchmark
// above is what hits the > 5K eval/s target.
func BenchmarkEvaluateForResource_ColdMiss(b *testing.B) {
	pack, err := LoadRulePackFromDir(sampleDir)
	if err != nil {
		b.Fatalf("LoadRulePackFromDir: %v", err)
	}

	cache, err := NewCache(1000, nil)
	if err != nil {
		b.Fatal(err)
	}
	e := New(WithRouter(FromRulePacks(pack)), WithCache(cache))
	if err := pack.RegisterTo(context.Background(), e); err != nil {
		b.Fatalf("RegisterTo: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := graph.Resource{
			Kind:            "Foo",
			Name:            "bench",
			Namespace:       "demo",
			GroupVersion:    "example.com/v1",
			UID:             types.UID(fmt.Sprintf("uid-%d", i)),
			ResourceVersion: fmt.Sprintf("%d", i),
		}
		if _, err := e.EvaluateForResource(context.Background(), r); err != nil {
			b.Fatalf("cold miss: %v", err)
		}
	}
}

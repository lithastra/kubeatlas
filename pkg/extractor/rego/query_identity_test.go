// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

func queryIdentitySource() string {
	first := strings.Replace(partialModule("first"), "derive :=", "first :=", 1)
	second := strings.SplitN(partialModule("second"), "derive :=", 2)[1]
	return first + "\nsecond :=" + second
}

func TestRuleCacheSeparatesEntrypoints(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("cluster=%s/reverse=%t", cluster, reverse), func(t *testing.T) {
				names := []string{"first", "second"}
				if reverse {
					names[0], names[1] = names[1], names[0]
				}
				pack := &RulePack{Name: "entrypoints"}
				for _, name := range names {
					pack.Modules = append(pack.Modules, &ModuleSpec{Name: name, Source: queryIdentitySource(), Entrypoint: "data.partial." + name, Match: GVKMatch{Group: "example.com", Kind: "Foo"}})
				}
				metrics := NewMetrics()
				cache, err := NewCache(10, metrics)
				if err != nil {
					t.Fatal(err)
				}
				engine := newSilentEngine(t, WithRouter(FromRulePacks(pack)), WithCache(cache), WithMetrics(metrics))
				if err := pack.RegisterTo(context.Background(), engine); err != nil {
					t.Fatal(err)
				}
				r := fooResource(1, "opaque-version")
				r.ClusterID = cluster
				for delivery := range 2 {
					edges, err := engine.EvaluateForResource(context.Background(), r)
					if err != nil || len(edges) != 2 {
						t.Fatal("entrypoint evaluation failed", edges, err)
					}
					for i, name := range names {
						if edges[i].To != "demo/Bar/"+name {
							t.Fatal("same-source entrypoints shared the wrong cached output", edges)
						}
					}
					m := metrics.Snapshot()
					if cache.Len() != 2 || m.CacheMisses != 2 || m.CacheHits != uint64(delivery*2) {
						t.Fatal("entrypoint identity changed cache accounting", cache.Len(), m)
					}
				}
			})
		}
	}
}

func TestRuleCacheReloadKeepsCompiledQueryIdentity(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, mode := range []string{"source", "entrypoint"} {
			t.Run(fmt.Sprintf("cluster=%s/%s", cluster, mode), func(t *testing.T) {
				testRuleCacheReloadIdentity(t, cluster, mode)
			})
		}
	}
}

func testRuleCacheReloadIdentity(t *testing.T, cluster, mode string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router := NewRouter()
	router.Register("reload", &ModuleSpec{Name: "rule", Match: GVKMatch{Group: "example.com", Kind: "Foo"}})
	metrics := NewMetrics()
	cache, err := NewCache(10, metrics)
	if err != nil {
		t.Fatal(err)
	}
	e := newSilentEngine(t, WithRouter(router), WithCache(cache), WithMetrics(metrics))
	source, entrypoint := partialModule("first"), "data.partial.derive"
	newSource, newEntrypoint := partialModule("second"), entrypoint
	if mode == "entrypoint" {
		source, entrypoint = queryIdentitySource(), "data.partial.first"
		newSource, newEntrypoint = source, "data.partial.second"
	}
	load := func(source, entrypoint string) {
		t.Helper()
		if err := e.LoadModule(ctx, "reload/rule", source, entrypoint); err != nil {
			t.Fatal(err)
		}
	}
	load(source, entrypoint)
	r := fooResource(1, "opaque-version")
	r.ClusterID = cluster
	assertTarget := func(edges []graph.Edge, err error, target string) {
		t.Helper()
		if err != nil || len(edges) != 1 || edges[0].To != "demo/Bar/"+target {
			t.Fatal("compiled query and cache identity diverged", target, edges, err)
		}
	}
	edges, err := e.EvaluateForResource(ctx, r)
	assertTarget(edges, err, "first")
	keys := cache.lru.Keys()
	if len(keys) != 1 {
		t.Fatal("fixture did not capture a single old query key", keys)
	}
	oldKey := keys[0]
	cache.Purge()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	leader := make(chan error, 1)
	failed := errors.New("injected original leader failure")
	go func() {
		_, err := cache.GetOrEvaluate(ctx, oldKey, func(context.Context) (CacheValue, error) {
			close(entered)
			<-release
			return CacheValue{}, failed
		})
		leader <- err
	}()
	awaitCacheSignal(t, entered)
	waiting := make(chan struct{}, 1)
	pending := make(chan cacheHandoffResult, 1)
	go func() {
		edges, err := e.EvaluateForResource(cacheWaitContext{Context: ctx, waiting: waiting}, r)
		pending <- cacheHandoffResult{value: CacheValue{Edges: edges}, err: err}
	}()
	// The call has selected the old compiled module and is waiting on its key.
	awaitCacheSignal(t, waiting)
	load(newSource, newEntrypoint)
	current := make(chan cacheHandoffResult, 1)
	go func() {
		edges, err := e.EvaluateForResource(ctx, r)
		current <- cacheHandoffResult{value: CacheValue{Edges: edges}, err: err}
	}()
	// The new query must complete independently of the old query's blocked key.
	newResult := awaitCacheSignal(t, current)
	assertTarget(newResult.value.Edges, newResult.err, "second")
	releaseOnce.Do(func() { close(release) })
	if err := awaitCacheSignal(t, leader); !errors.Is(err, failed) {
		t.Fatal("fixture leader lost its failure", err)
	}
	result := awaitCacheSignal(t, pending)
	assertTarget(result.value.Edges, result.err, "first")
	load(source, entrypoint)
	edges, err = e.EvaluateForResource(ctx, r)
	assertTarget(edges, err, "first")
	// Failed compilation keeps the existing registration and its usable cache.
	if err := e.LoadModule(ctx, "reload/rule", "invalid rego", entrypoint); err == nil {
		t.Fatal("fixture invalid reload unexpectedly compiled")
	}
	edges, err = e.EvaluateForResource(ctx, r)
	assertTarget(edges, err, "first")
	m := metrics.Snapshot()
	if cache.Len() != 2 || m.CacheMisses != 4 || m.CacheHits != 2 {
		t.Fatal("reload poisoned cache identity or forced unrelated invalidation", cache.Len(), m)
	}
	assertCacheFlightCount(t, cache, 0)
}

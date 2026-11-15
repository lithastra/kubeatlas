// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func representationModule() string {
	return strings.Replace(partialModule("target"), `"name":"target"`, `"name":replace(input.apiVersion, "/", "-")`, 1)
}

func representationEngine(t *testing.T, wildcard bool) (*Engine, *Metrics) {
	t.Helper()
	match := GVKMatch{Group: "example.com", Kind: "Foo"}
	if wildcard {
		match = GVKMatch{}
	}
	router := NewRouter()
	router.Register("representation", &ModuleSpec{Name: "version", Match: match})
	metrics := NewMetrics()
	cache, err := NewCache(10, metrics)
	if err != nil {
		t.Fatal(err)
	}
	e := newSilentEngine(t, WithRouter(router), WithCache(cache), WithMetrics(metrics))
	if err := e.LoadModule(context.Background(), "representation/version", representationModule(), "data.partial.derive"); err != nil {
		t.Fatal(err)
	}
	return e, metrics
}

func TestResourceEvaluationSeparatesAPIRepresentations(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, wildcard := range []bool{false, true} {
			for _, raw := range []bool{false, true} {
				for _, reverse := range []bool{false, true} {
					t.Run(fmt.Sprintf("cluster=%s/wildcard=%t/raw=%t/reverse=%t", cluster, wildcard, raw, reverse), func(t *testing.T) {
						e, metrics := representationEngine(t, wildcard)
						versions := []string{"example.com/v1", "example.com/v2"}
						if reverse {
							versions[0], versions[1] = versions[1], versions[0]
						}
						for delivery := range 4 {
							r := fooResource(1, "same-opaque-version")
							r.ClusterID, r.GroupVersion = cluster, versions[delivery%2]
							if raw {
								r.Raw = map[string]any{"apiVersion": r.GroupVersion, "kind": r.Kind, "metadata": map[string]any{
									"namespace": r.Namespace, "name": r.Name, "uid": string(r.UID), "resourceVersion": r.ResourceVersion,
								}}
							}
							edges, err := e.EvaluateForResource(context.Background(), r)
							want := "demo/Bar/" + strings.ReplaceAll(r.GroupVersion, "/", "-")
							if err != nil || len(edges) != 1 || edges[0].From != "demo/Foo/foo-001" || edges[0].To != want {
								t.Fatal("served API representations shared rule output", r.GroupVersion, edges, err)
							}
							m := metrics.Snapshot()
							if m.CacheMisses != uint64(min(delivery+1, 2)) || m.CacheHits != uint64(max(delivery-1, 0)) || e.cache.Len() != min(delivery+1, 2) {
								t.Fatal("representation replay changed cache accounting", e.cache.Len(), m)
							}
						}
						assertCacheFlightCount(t, e.cache, 0)
					})
				}
			}
		}
	}
}

func TestResourceEvaluationVersionDoesNotWaitOnOtherRepresentation(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		t.Run("cluster="+cluster, func(t *testing.T) {
			e, metrics := representationEngine(t, false)
			first := fooResource(1, "same-opaque-version")
			first.ClusterID = cluster
			if _, err := e.EvaluateForResource(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			key := e.cache.lru.Keys()[0]
			e.cache.Purge()
			done := make(chan struct{})
			e.cache.inflight[key] = done
			defer func() { delete(e.cache.inflight, key); close(done) }()
			second := first
			second.GroupVersion = "example.com/v2"
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			edges, err := e.EvaluateForResource(ctx, second)
			if err != nil || len(edges) != 1 || edges[0].To != "demo/Bar/example.com-v2" {
				t.Fatal("new representation waited on the old representation's key", edges, err)
			}
			m := metrics.Snapshot()
			if m.CacheMisses != 2 || m.CacheHits != 0 || e.cache.Len() != 1 {
				t.Fatal("independent representation changed cache accounting", e.cache.Len(), m)
			}
		})
	}
}

func TestResourceEvaluationWildcardSeparatesAPIGroups(t *testing.T) {
	e, metrics := representationEngine(t, true)
	for _, apiVersion := range []string{"v1", "example.com/v1", "other.example/v1", "v1"} {
		r := fooResource(1, "same-opaque-version")
		r.GroupVersion = apiVersion
		edges, err := e.EvaluateForResource(context.Background(), r)
		if err != nil || len(edges) != 1 || edges[0].To != "demo/Bar/"+strings.ReplaceAll(apiVersion, "/", "-") {
			t.Fatal("wildcard reused output across API groups", edges, err)
		}
	}
	if m := metrics.Snapshot(); m.CacheMisses != 3 || m.CacheHits != 1 || e.cache.Len() != 3 {
		t.Fatal("full API version identity was not retained", e.cache.Len(), m)
	}
}

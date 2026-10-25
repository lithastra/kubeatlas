// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"errors"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// defaultCacheSize is the LRU cap. Sized to comfortably hold the
// (cluster, UID, resourceVersion, ruleHash) entries for a 5K-resource cluster
// times a handful of rules. Operators can override via Engine option
// (P2-T9 step 3 keeps this small and configurable rather than
// unbounded — guide §1.7 perf budget assumes warm cache, never
// cold). Anti-pattern: sync.Map / unbounded map → memory leak on
// long-running deployments.
const defaultCacheSize = 10000

// CacheKey identifies a single (resource, rule) evaluation. Splitting
// resource UID from resourceVersion is what lets the same resource
// across versions invalidate cleanly: bumping spec → new
// resourceVersion → guaranteed cache miss for every rule.
//
// RuleHash is the sha256 of the .rego source. Bundling it in the key
// means a rule-pack reload that changes one .rego silently
// invalidates only its own cache entries; other rules keep their
// hits. (Anti-pattern: keying on resource content hash instead of
// resourceVersion — a re-upsert with identical content but bumped
// resourceVersion would mistakenly hit. K8s guarantees
// resourceVersion is monotonic on real change, so we trust it.)
type CacheKey struct {
	ClusterID       string
	UID             string
	ResourceVersion string
	RuleHash        string
}

// CacheValue is what the cache stores per (resource, rule). Errors
// are NOT cached: a transient OPA panic or timeout shouldn't poison
// the cache for the lifetime of the resourceVersion.
type CacheValue struct {
	Edges []graph.Edge
}

// Cache is the per-engine evaluation cache. Wraps an LRU and a small
// metrics handle so GetOrEvaluate can attribute hit/miss without
// callers threading metrics through every call site.
type Cache struct {
	lru     *lru.Cache[CacheKey, CacheValue]
	metrics *Metrics

	// inflight de-dups concurrent evaluations of the same key so a
	// burst of identical informer events fans into one OPA call.
	mu       sync.Mutex
	inflight map[CacheKey]chan struct{}
}

// NewCache returns a cache with the given size. size <= 0 falls back
// to defaultCacheSize so callers don't accidentally end up with a
// 1-entry LRU after a bad config.
func NewCache(size int, metrics *Metrics) (*Cache, error) {
	if size <= 0 {
		size = defaultCacheSize
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	l, err := lru.New[CacheKey, CacheValue](size)
	if err != nil {
		return nil, err
	}
	return &Cache{
		lru:      l,
		metrics:  metrics,
		inflight: make(map[CacheKey]chan struct{}),
	}, nil
}

// GetOrEvaluate returns the cached value for key or invokes
// evaluateFn on miss. evaluateFn errors are returned directly and
// not cached, so transient failures retry on the next event.
//
// Concurrent calls for the same key are de-duped: the first caller
// runs evaluateFn, the rest wait for completion or their own cancellation.
// After a failed leader, active waiters contend for one replacement evaluation;
// each caller invokes evaluateFn at most once and failures are never cached.
// Cancellation is checked before returning hits and after evaluation, excluding
// results from a dependency that completes after its caller was cancelled.
func (c *Cache) GetOrEvaluate(
	ctx context.Context,
	key CacheKey,
	evaluateFn func(context.Context) (CacheValue, error),
) (CacheValue, error) {
	if c == nil {
		return CacheValue{}, errors.New("rego.Cache.GetOrEvaluate: nil cache")
	}

	for {
		if err := ctx.Err(); err != nil {
			return CacheValue{}, err
		}
		// Preserve the warm-hit path without taking the flight-map mutex.
		if v, ok := c.lru.Get(key); ok {
			if err := ctx.Err(); err != nil {
				return CacheValue{}, err
			}
			c.metrics.IncCacheHit()
			return v, nil
		}

		// A wake-up always returns to the locked ownership decision. Failed
		// leaders must not let followers overwrite the map without its lock.
		c.mu.Lock()
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return CacheValue{}, err
		}
		if done, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return CacheValue{}, ctx.Err()
			case <-done:
				continue
			}
		}
		// A leader may have published and retired between the fast-path miss
		// and this lock acquisition. Reuse its value instead of evaluating twice.
		if v, ok := c.lru.Get(key); ok {
			c.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return CacheValue{}, err
			}
			c.metrics.IncCacheHit()
			return v, nil
		}
		done := make(chan struct{})
		c.inflight[key] = done
		c.mu.Unlock()

		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			close(done)
			c.mu.Unlock()
		}()
		if err := ctx.Err(); err != nil {
			return CacheValue{}, err
		}
		c.metrics.IncCacheMiss()
		v, err := evaluateFn(ctx)
		if ctx.Err() != nil {
			return CacheValue{}, ctx.Err()
		}
		if err != nil {
			return CacheValue{}, err
		}
		c.lru.Add(key, v)
		return v, nil
	}
}

// Len reports the current number of cached entries. Test helper
// + future /metrics gauge.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	return c.lru.Len()
}

// Purge drops every cached entry. Used by tests; future runtime
// callers may use it on rule-pack reload to clear stale results.
func (c *Cache) Purge() {
	if c == nil {
		return
	}
	c.lru.Purge()
}

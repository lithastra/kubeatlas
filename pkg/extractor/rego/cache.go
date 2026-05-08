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
// (UID, resourceVersion, ruleHash) entries for a 5K-resource cluster
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
	inflight map[CacheKey]*sync.WaitGroup
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
		inflight: make(map[CacheKey]*sync.WaitGroup),
	}, nil
}

// GetOrEvaluate returns the cached value for key or invokes
// evaluateFn on miss. evaluateFn errors are returned directly and
// not cached, so transient failures retry on the next event.
//
// Concurrent calls for the same key are de-duped: the first caller
// runs evaluateFn, the rest wait on a WaitGroup and consume the
// cached result. This matters under informer fan-out where a single
// resource update can produce N near-simultaneous extract calls.
func (c *Cache) GetOrEvaluate(
	ctx context.Context,
	key CacheKey,
	evaluateFn func(context.Context) (CacheValue, error),
) (CacheValue, error) {
	if c == nil {
		return CacheValue{}, errors.New("rego.Cache.GetOrEvaluate: nil cache")
	}

	if v, ok := c.lru.Get(key); ok {
		c.metrics.IncCacheHit()
		return v, nil
	}

	// Miss path — guard against concurrent identical evaluations.
	c.mu.Lock()
	if wg, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		wg.Wait()
		// Re-check the cache: the leader either populated it or
		// failed (in which case we also try, returning the
		// follower's own error rather than racing on the leader's).
		if v, ok := c.lru.Get(key); ok {
			c.metrics.IncCacheHit()
			return v, nil
		}
		// Leader failed; fall through to do our own evaluation.
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	c.inflight[key] = wg
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.inflight, key)
		c.mu.Unlock()
		wg.Done()
	}()

	c.metrics.IncCacheMiss()
	v, err := evaluateFn(ctx)
	if err != nil {
		return CacheValue{}, err
	}
	c.lru.Add(key, v)
	return v, nil
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

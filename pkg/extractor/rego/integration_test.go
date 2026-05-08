// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"fmt"
	"testing"

	"github.com/open-policy-agent/opa/v1/rego"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// fakeResultSet builds the minimal ResultSet shape decodeEdges
// consumes (one Result, one Expression, value = caller's input).
// Lets the bad-shape tests bypass the Engine + sample pack roundtrip.
func fakeResultSet(value any) rego.ResultSet {
	return rego.ResultSet{
		{Expressions: []*rego.ExpressionValue{{Value: value}}},
	}
}

// newWiredEngine builds an Engine with the sample rule pack loaded
// plus a router and cache wired up — the shape main.go (P2-T11) will
// produce. Returned alongside the metrics handle so tests can assert
// hit/miss counters directly.
func newWiredEngine(t *testing.T) (*Engine, *Metrics) {
	t.Helper()
	pack, err := LoadRulePackFromDir(sampleDir)
	if err != nil {
		t.Fatalf("LoadRulePackFromDir: %v", err)
	}

	metrics := NewMetrics()
	cache, err := NewCache(1000, metrics)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	router := FromRulePacks(pack)

	silent := newSilentEngine(t,
		WithRouter(router),
		WithCache(cache),
		WithMetrics(metrics),
	)
	if err := pack.RegisterTo(context.Background(), silent); err != nil {
		t.Fatalf("RegisterTo: %v", err)
	}
	return silent, metrics
}

// fooResource builds a graph.Resource that matches the sample pack's
// {group: example.com, kind: Foo} declaration.
func fooResource(i int, resourceVersion string) graph.Resource {
	return graph.Resource{
		Kind:            "Foo",
		Name:            fmt.Sprintf("foo-%03d", i),
		Namespace:       "demo",
		GroupVersion:    "example.com/v1",
		UID:             types.UID(fmt.Sprintf("uid-%03d", i)),
		ResourceVersion: resourceVersion,
	}
}

// barResource matches kind=Bar; the sample pack does NOT register
// for Bar, so the router skips it (zero OPA calls).
func barResource(i int, resourceVersion string) graph.Resource {
	return graph.Resource{
		Kind:            "Bar",
		Name:            fmt.Sprintf("bar-%03d", i),
		Namespace:       "demo",
		GroupVersion:    "example.com/v1",
		UID:             types.UID(fmt.Sprintf("baruid-%03d", i)),
		ResourceVersion: resourceVersion,
	}
}

// TestEvaluateForResource_HappyPath: a single Foo resource produces
// the documented Foo→Bar edge.
func TestEvaluateForResource_HappyPath(t *testing.T) {
	e, _ := newWiredEngine(t)
	edges, err := e.EvaluateForResource(context.Background(), fooResource(1, "1"))
	if err != nil {
		t.Fatalf("EvaluateForResource: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	want := graph.Edge{
		From: "demo/Foo/foo-001",
		To:   "demo/Bar/foo-001-target",
		Type: "DERIVED_TO",
	}
	if edges[0] != want {
		t.Errorf("edge = %+v, want %+v", edges[0], want)
	}
}

// TestEvaluateForResource_CacheHits: 100 Foo resources, then re-run
// with the same resourceVersion. First pass = 100 misses; second
// pass = 100 hits, zero new misses.
func TestEvaluateForResource_CacheHits(t *testing.T) {
	e, metrics := newWiredEngine(t)
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		_, err := e.EvaluateForResource(ctx, fooResource(i, "1"))
		if err != nil {
			t.Fatalf("first pass i=%d: %v", i, err)
		}
	}
	first := metrics.Snapshot()
	if first.CacheMisses != 100 || first.CacheHits != 0 {
		t.Errorf("after first pass: misses=%d hits=%d, want 100/0",
			first.CacheMisses, first.CacheHits)
	}

	// Second pass — same UID, same ResourceVersion → 100 hits, no
	// new misses.
	for i := 0; i < 100; i++ {
		_, err := e.EvaluateForResource(ctx, fooResource(i, "1"))
		if err != nil {
			t.Fatalf("second pass i=%d: %v", i, err)
		}
	}
	second := metrics.Snapshot()
	if second.CacheHits-first.CacheHits != 100 {
		t.Errorf("second pass hits delta = %d, want 100",
			second.CacheHits-first.CacheHits)
	}
	if second.CacheMisses != first.CacheMisses {
		t.Errorf("second pass produced %d new misses, want 0",
			second.CacheMisses-first.CacheMisses)
	}
}

// TestEvaluateForResource_ResourceVersionInvalidates: bumping
// resourceVersion forces a fresh evaluation even for the same UID.
func TestEvaluateForResource_ResourceVersionInvalidates(t *testing.T) {
	e, metrics := newWiredEngine(t)
	ctx := context.Background()

	_, err := e.EvaluateForResource(ctx, fooResource(1, "1"))
	if err != nil {
		t.Fatal(err)
	}
	missesAfterV1 := metrics.Snapshot().CacheMisses

	// Same UID, bumped resourceVersion.
	_, err = e.EvaluateForResource(ctx, fooResource(1, "2"))
	if err != nil {
		t.Fatal(err)
	}
	missesAfterV2 := metrics.Snapshot().CacheMisses

	if missesAfterV2-missesAfterV1 != 1 {
		t.Errorf("resourceVersion bump: misses delta = %d, want 1",
			missesAfterV2-missesAfterV1)
	}
}

// TestEvaluateForResource_GVKRouterFilter: the sample pack registers
// only for kind=Foo; pumping 100 Bar resources must NOT trigger any
// OPA evaluation (cache miss counter stays at 0).
func TestEvaluateForResource_GVKRouterFilter(t *testing.T) {
	e, metrics := newWiredEngine(t)
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		edges, err := e.EvaluateForResource(ctx, barResource(i, "1"))
		if err != nil {
			t.Fatalf("Bar i=%d: %v", i, err)
		}
		if len(edges) != 0 {
			t.Errorf("Bar produced %d edges, want 0", len(edges))
		}
	}
	snap := metrics.Snapshot()
	if snap.CacheHits != 0 || snap.CacheMisses != 0 {
		t.Errorf("router filter: hits=%d misses=%d, want 0/0",
			snap.CacheHits, snap.CacheMisses)
	}
}

// TestEvaluateForResource_NoRouterReturnsError: building an engine
// without a router and calling the resource path is a programming
// error — surface it loudly.
func TestEvaluateForResource_NoRouterReturnsError(t *testing.T) {
	e := newSilentEngine(t)
	_, err := e.EvaluateForResource(context.Background(), fooResource(1, "1"))
	if err == nil {
		t.Fatal("expected no-router error, got nil")
	}
}

// TestRouter_Match: lookup hits the right slot; wildcard module
// applies to every GVK.
func TestRouter_Match(t *testing.T) {
	rp := &RulePack{
		Name: "p",
		Modules: []*ModuleSpec{
			{Name: "foo", Match: GVKMatch{Group: "example.com", Kind: "Foo"}},
			{Name: "any", Match: GVKMatch{}},
		},
	}
	r := FromRulePacks(rp)
	if r.Size() != 2 {
		t.Errorf("Size = %d, want 2", r.Size())
	}

	got := r.Match(GVK{Group: "example.com", Kind: "Foo"})
	if len(got) != 2 {
		t.Errorf("Foo match: %d entries, want 2 (specific + wildcard)", len(got))
	}

	got = r.Match(GVK{Kind: "Bar"})
	if len(got) != 1 || got[0].Name != "p/any" {
		t.Errorf("Bar match: %+v, want [p/any]", got)
	}
}

// TestGVKOf parses GroupVersion correctly: core/v1 (no slash) → empty
// group; "apps/v1" → "apps".
func TestGVKOf(t *testing.T) {
	cases := []struct {
		gv, kind, wantGroup string
	}{
		{"v1", "Pod", ""},
		{"apps/v1", "Deployment", "apps"},
		{"example.com/v1", "Foo", "example.com"},
		{"", "Namespace", ""},
	}
	for _, c := range cases {
		got := GVKOf(graph.Resource{Kind: c.kind, GroupVersion: c.gv})
		if got.Group != c.wantGroup || got.Kind != c.kind {
			t.Errorf("GVKOf({%q, %q}) = %+v, want group=%q kind=%q",
				c.kind, c.gv, got, c.wantGroup, c.kind)
		}
	}
}

// TestCache_GetOrEvaluate covers the LRU + miss path directly;
// useful when EvaluateForResource is not the right abstraction
// (synthetic edges, error injection).
func TestCache_GetOrEvaluate(t *testing.T) {
	metrics := NewMetrics()
	c, err := NewCache(10, metrics)
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	fn := func(ctx context.Context) (CacheValue, error) {
		calls++
		return CacheValue{Edges: []graph.Edge{{From: "a", To: "b", Type: "X"}}}, nil
	}
	key := CacheKey{UID: "u1", ResourceVersion: "1", RuleHash: "h1"}

	// First call: miss + invoke fn.
	v, err := c.GetOrEvaluate(context.Background(), key, fn)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(v.Edges) != 1 {
		t.Errorf("first call: calls=%d edges=%d, want 1/1", calls, len(v.Edges))
	}

	// Second call: hit, fn not invoked.
	_, err = c.GetOrEvaluate(context.Background(), key, fn)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("second call: calls=%d, want 1 (hit should not invoke fn)", calls)
	}

	snap := metrics.Snapshot()
	if snap.CacheMisses != 1 || snap.CacheHits != 1 {
		t.Errorf("metrics: misses=%d hits=%d, want 1/1", snap.CacheMisses, snap.CacheHits)
	}
}

// TestCache_NilSafety: calling methods on a nil *Cache fails cleanly
// rather than panicking — defensive against bootstrap-time bugs.
func TestCache_NilSafety(t *testing.T) {
	var c *Cache
	if c.Len() != 0 {
		t.Errorf("nil.Len() = %d, want 0", c.Len())
	}
	c.Purge() // must not panic
	_, err := c.GetOrEvaluate(context.Background(), CacheKey{},
		func(ctx context.Context) (CacheValue, error) { return CacheValue{}, nil })
	if err == nil {
		t.Error("nil cache GetOrEvaluate: expected error, got nil")
	}
}

// TestMetrics_Increment: exercise every Inc* method so the snapshot
// reflects writes accurately.
func TestMetrics_Increment(t *testing.T) {
	m := NewMetrics()
	m.IncCacheHit()
	m.IncCacheHit()
	m.IncCacheMiss()
	m.IncEvalTimeout()
	m.IncEvalTimeout()
	m.IncEvalTimeout()
	m.IncEvalPanic()

	s := m.Snapshot()
	if s.CacheHits != 2 || s.CacheMisses != 1 || s.EvalTimeouts != 3 || s.EvalPanics != 1 {
		t.Errorf("snapshot = %+v", s)
	}
}

// TestDecodeEdges_BadShape covers the error branches: non-map entry,
// missing type / from / to, missing endpoint fields. None of these
// are valid rule outputs; surfacing them stops a buggy rule from
// silently producing zero edges.
func TestDecodeEdges_BadShape(t *testing.T) {
	cases := []struct {
		name string
		in   any
	}{
		{"non-map element", []any{"oops"}},
		{"missing type", []any{map[string]any{"from": map[string]any{"kind": "K", "name": "n"}, "to": map[string]any{"kind": "K", "name": "m"}}}},
		{"from missing kind", []any{map[string]any{"type": "T", "from": map[string]any{"name": "n"}, "to": map[string]any{"kind": "K", "name": "m"}}}},
		{"to missing name", []any{map[string]any{"type": "T", "from": map[string]any{"kind": "K", "name": "n"}, "to": map[string]any{"kind": "K"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := decodeEdges(fakeResultSet(c.in))
			if err == nil {
				t.Errorf("expected error for %s, got nil", c.name)
			}
		})
	}
}

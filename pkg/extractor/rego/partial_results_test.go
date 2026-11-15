// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/open-policy-agent/opa/v1/ast"
	oparego "github.com/open-policy-agent/opa/v1/rego"
	"github.com/open-policy-agent/opa/v1/types"
)

func partialModule(target string) string {
	return fmt.Sprintf(`package partial
import rego.v1
derive := [{"type":"OWNS", "from":{"kind":input.kind,"namespace":input.metadata.namespace,"name":input.metadata.name}, "to":{"kind":"Bar","namespace":input.metadata.namespace,"name":%q}}]
`, target)
}

func TestResourceEvaluationRetainsOnlyCompletedModules(t *testing.T) {
	for _, mode := range []string{"decode", "missing", "eval", "panic", "cancel"} {
		for _, position := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/position=%d", mode, position), func(t *testing.T) {
				testResourceEvaluationPartial(t, mode, position)
			})
		}
	}
}

func testResourceEvaluationPartial(t *testing.T, mode string, position int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router := NewRouter()
	metrics := NewMetrics()
	cache, err := NewCache(100, metrics)
	if err != nil {
		t.Fatal(err)
	}
	e := newSilentEngine(t, WithRouter(router), WithCache(cache), WithMetrics(metrics))
	for i := range 3 {
		name := fmt.Sprintf("rule-%d", i)
		router.Register("partial", &ModuleSpec{Name: name, Match: GVKMatch{Group: "example.com", Kind: "Foo"}})
		if mode == "missing" && i == position {
			continue
		}
		source := partialModule(name)
		if mode == "decode" && i == position {
			// A valid prefix inside a malformed module must remain atomic.
			source = strings.Replace(source, "]\n", ", {}]\n", 1)
		}
		if err := e.LoadModule(ctx, "partial/"+name, source, "data.partial.derive"); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "eval" || mode == "panic" || mode == "cancel" {
		query, err := oparego.New(
			oparego.Query(`test.failure("trigger")`),
			oparego.StrictBuiltinErrors(true),
			oparego.Function1(&oparego.Function{
				Name: "test.failure", Decl: types.NewFunction(types.Args(types.S), types.S),
			}, func(_ oparego.BuiltinContext, arg *ast.Term) (*ast.Term, error) {
				switch mode {
				case "eval":
					return nil, errors.New("injected module evaluation failure")
				case "panic":
					panic("injected module panic")
				default:
					cancel()
					return arg, nil
				}
			}),
		).PrepareForEval(ctx)
		if err != nil {
			t.Fatal(err)
		}
		e.modules[fmt.Sprintf("partial/rule-%d", position)].query = query
	}
	r := fooResource(1, "opaque-version")
	r.ClusterID = "east"
	for delivery := range 2 {
		edges, err := e.EvaluateForResource(ctx, r)
		wantEdges := position
		if mode == "cancel" {
			wantEdges = 0
			if !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost its cause", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("partial/rule-%d", position)) {
			t.Fatal("failure lost its module identity", err)
		}
		if mode == "panic" && !errors.Is(err, ErrEvalPanic) {
			t.Fatal("failure lost its panic cause", err)
		}
		if len(edges) != wantEdges {
			t.Fatal("lost completed modules or exposed failed/later output", edges, err)
		}
		for i, edge := range edges {
			if edge.From != "demo/Foo/foo-001" || edge.To != fmt.Sprintf("demo/Bar/rule-%d", i) {
				t.Fatal("changed completed output", edge)
			}
		}
		if cache.Len() != position {
			t.Fatal("failed/later module entered the cache", cache.Len())
		}
		m := metrics.Snapshot()
		wantMisses := uint64(position)
		if mode != "missing" {
			wantMisses += uint64(delivery + 1)
		}
		wantHits := uint64(delivery * position)
		if mode == "cancel" {
			wantMisses, wantHits = uint64(position+1), 0
		}
		if m.CacheMisses != wantMisses || m.CacheHits != wantHits {
			t.Fatal("failure retried later modules or consumed a cancelled cache hit", m)
		}
	}
	// A subsequent active delivery may recover, without re-running cached
	// successful modules or having cached the failed module's valid prefix.
	if err := e.LoadModule(context.Background(), fmt.Sprintf("partial/rule-%d", position), partialModule("recovered"), "data.partial.derive"); err != nil {
		t.Fatal(err)
	}
	edges, err := e.EvaluateForResource(context.Background(), r)
	if err != nil || len(edges) != 3 || cache.Len() != 3 || edges[position].To != "demo/Bar/recovered" {
		t.Fatal("active delivery did not recover all modules", edges, err)
	}
}

func TestResourceEvaluationCancelledWithoutMatches(t *testing.T) {
	e, metrics := newWiredEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	edges, err := e.EvaluateForResource(ctx, barResource(1, "1"))
	if !errors.Is(err, context.Canceled) || len(edges) != 0 || metrics.Snapshot().CacheMisses != 0 {
		t.Fatal("unmatched cancellation appeared successful or ran a rule", edges, err)
	}
}

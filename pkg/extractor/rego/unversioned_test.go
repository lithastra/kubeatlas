// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/open-policy-agent/opa/v1/ast"
	oparego "github.com/open-policy-agent/opa/v1/rego"
	"github.com/open-policy-agent/opa/v1/types"
)

func unversionedEngine(t *testing.T) (*Engine, *Metrics) {
	t.Helper()
	router := NewRouter()
	router.Register("identity", &ModuleSpec{Name: "labels"}) // Also exercise absent API versions.
	metrics := NewMetrics()
	cache, err := NewCache(10, metrics)
	if err != nil {
		t.Fatal(err)
	}
	e := newSilentEngine(t, WithRouter(router), WithCache(cache), WithMetrics(metrics))
	source := strings.Replace(partialModule("target"), `"name":"target"`, `"name":input.metadata.labels.target`, 1)
	if err := e.LoadModule(context.Background(), "identity/labels", source, "data.partial.derive"); err != nil {
		t.Fatal(err)
	}
	return e, metrics
}

func withoutVersionIdentity(r graph.Resource, missing string) graph.Resource {
	switch missing {
	case "uid":
		r.UID = ""
	case "version":
		r.ResourceVersion = ""
	case "api-version":
		r.GroupVersion = ""
	}
	return r
}

func TestUnversionedEvaluationDoesNotConsumeOrPopulateCache(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, missing := range []string{"uid", "version", "api-version"} {
			t.Run(fmt.Sprintf("cluster=%s/missing=%s", cluster, missing), func(t *testing.T) {
				e, metrics := unversionedEngine(t)
				full := fooResource(1, "opaque-version")
				full.ClusterID = cluster
				check := func(r graph.Resource, target string) {
					t.Helper()
					r.Labels = map[string]string{"target": target}
					edges, err := e.EvaluateForResource(context.Background(), r)
					if err != nil || len(edges) != 1 || edges[0].From != r.Namespace+"/"+r.Kind+"/"+r.Name || edges[0].To != r.Namespace+"/Bar/"+target {
						t.Fatal("incomplete identity reused another observation's output", edges, err)
					}
				}
				incomplete := withoutVersionIdentity(full, missing)
				check(incomplete, "first")
				check(incomplete, "second")
				other := withoutVersionIdentity(fooResource(2, "opaque-version"), missing)
				other.ClusterID = cluster
				check(other, "other-object")
				if e.cache.Len() != 0 || metrics.Snapshot() != (MetricsSnapshot{}) {
					t.Fatal("unversioned calls entered the cache or cache accounting", e.cache.Len(), metrics.Snapshot())
				}
				check(full, "qualified")
				check(full, "qualified")
				check(incomplete, "fresh-unqualified")
				check(full, "qualified")
				m := metrics.Snapshot()
				if e.cache.Len() != 1 || m.CacheMisses != 1 || m.CacheHits != 2 {
					t.Fatal("incomplete delivery poisoned or evicted a qualified cache entry", e.cache.Len(), m)
				}
				assertCacheFlightCount(t, e.cache, 0)
			})
		}
	}
}

func TestUnversionedEvaluationSkipsUnprovableSharedFlight(t *testing.T) {
	for _, missing := range []string{"uid", "version", "api-version"} {
		t.Run(missing, func(t *testing.T) {
			e, metrics := unversionedEngine(t)
			r := withoutVersionIdentity(fooResource(1, "opaque-version"), missing)
			r.Labels = map[string]string{"target": "fresh"}
			meta := e.Loaded()[0]
			key := CacheKey{GroupVersion: r.GroupVersion, UID: string(r.UID), ResourceVersion: r.ResourceVersion, RuleHash: meta.RuleHash, Entrypoint: meta.Entrypoint}
			done := make(chan struct{})
			e.cache.inflight[key] = done
			defer func() { delete(e.cache.inflight, key); close(done) }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			// A synthetic old flight is never closed before the call completes.
			// Waiting on it is an identity error, rather than a valid deduplication.
			edges, err := e.EvaluateForResource(ctx, r)
			if err != nil || len(edges) != 1 || edges[0].To != "demo/Bar/fresh" {
				t.Fatal("incomplete observation consumed an unprovable flight", edges, err)
			}
			if e.cache.Len() != 0 || metrics.Snapshot() != (MetricsSnapshot{}) {
				t.Fatal("bypass changed cache state or counters", metrics.Snapshot())
			}
		})
	}
}

func TestUnversionedEvaluationCancelledAndMalformed(t *testing.T) {
	for _, mode := range []string{"cancelled", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			e, metrics := unversionedEngine(t)
			r := fooResource(1, "")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			} else if err := e.LoadModule(ctx, "identity/labels", "package partial\nimport rego.v1\nderive := [{\"type\":\"OWNS\"}]", "data.partial.derive"); err != nil {
				t.Fatal(err)
			}
			edges, err := e.EvaluateForResource(ctx, r)
			if err == nil || len(edges) != 0 || (mode == "cancelled" && !errors.Is(err, context.Canceled)) {
				t.Fatal("bypassed evaluation lost cancellation or atomic decoding", edges, err)
			}
			if e.cache.Len() != 0 || metrics.Snapshot() != (MetricsSnapshot{}) {
				t.Fatal("failed bypass entered the cache or accounting", metrics.Snapshot())
			}
		})
	}
}

func TestUnversionedEvaluationKeepsSandboxGuards(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout", "panic", "error"} {
		t.Run(mode, func(t *testing.T) {
			e, metrics := unversionedEngine(t)
			e.defaultTimeout = minTimeout
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			query, err := oparego.New(
				oparego.Query(`test.bypass("trigger")`),
				oparego.StrictBuiltinErrors(true),
				oparego.Function1(&oparego.Function{Name: "test.bypass", Decl: types.NewFunction(types.Args(types.S), types.S)},
					func(bctx oparego.BuiltinContext, arg *ast.Term) (*ast.Term, error) {
						switch mode {
						case "cancel":
							cancel()
							return arg, nil
						case "timeout":
							<-bctx.Context.Done()
							return nil, bctx.Context.Err()
						case "panic":
							panic("injected unversioned evaluation panic")
						default:
							return nil, errors.New("injected unversioned evaluation error")
						}
					}),
			).PrepareForEval(ctx)
			if err != nil {
				t.Fatal(err)
			}
			e.modules["identity/labels"].query = query
			edges, err := e.EvaluateForResource(ctx, fooResource(1, ""))
			want := map[string]error{"cancel": context.Canceled, "timeout": ErrEvalTimeout, "panic": ErrEvalPanic}[mode]
			if err == nil || len(edges) != 0 || (want != nil && !errors.Is(err, want)) {
				t.Fatal("cache bypass lost the evaluation guard or its cause", edges, err)
			}
			if mode == "error" && !strings.Contains(err.Error(), "injected unversioned evaluation error") {
				t.Fatal("cache bypass lost the active failure", err)
			}
			m := metrics.Snapshot()
			wantMetrics := MetricsSnapshot{}
			switch mode {
			case "timeout":
				wantMetrics.EvalTimeouts = 1
			case "panic":
				wantMetrics.EvalPanics = 1
			}
			if e.cache.Len() != 0 || m != wantMetrics {
				t.Fatal("bypass changed guard metrics or cache accounting", m)
			}
		})
	}
}

func TestUnversionedEvaluationRetainsCompletedModulePrefix(t *testing.T) {
	e, metrics := unversionedEngine(t)
	e.router.Register("identity", &ModuleSpec{Name: "malformed"})
	if err := e.LoadModule(context.Background(), "identity/malformed", "package broken\nimport rego.v1\nderive := [{\"type\":\"OWNS\"}]", "data.broken.derive"); err != nil {
		t.Fatal(err)
	}
	r := fooResource(1, "")
	r.Labels = map[string]string{"target": "retained"}
	edges, err := e.EvaluateForResource(context.Background(), r)
	if err == nil || len(edges) != 1 || edges[0].To != "demo/Bar/retained" {
		t.Fatal("uncached active failure discarded completed earlier modules", edges, err)
	}
	if e.cache.Len() != 0 || metrics.Snapshot() != (MetricsSnapshot{}) {
		t.Fatal("uncached partial extraction populated cache state", metrics.Snapshot())
	}
}

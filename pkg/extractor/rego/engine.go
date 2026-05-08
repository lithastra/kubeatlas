// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/open-policy-agent/opa/v1/rego"
)

// Hard bounds on the per-evaluation timeout. 50ms is short enough
// that a stuck rule cannot block the informer pipeline noticeably;
// 1s is long enough for the worst legitimate aggregation we expect
// (count() over a 5K-resource graph). Anything outside this range is
// clamped at WithTimeout time and a warn is logged.
const (
	minTimeout     = 50 * time.Millisecond
	maxTimeout     = time.Second
	defaultTimeout = 100 * time.Millisecond
)

// Engine compiles Rego modules once via PrepareForEval and evaluates
// them under the timeout + recover guards in sandbox.go. The hot
// path (Evaluate) never re-parses; that is the entire point of the
// type's existence (anti-pattern: building rego.New per call).
type Engine struct {
	mu             sync.RWMutex
	modules        map[string]*compiledModule
	logger         *slog.Logger
	defaultTimeout time.Duration
}

// compiledModule is the per-module record stored on the Engine.
// rego.PreparedEvalQuery itself is goroutine-safe (per OPA docs); we
// guard the map only, not the queries inside.
type compiledModule struct {
	query rego.PreparedEvalQuery
	meta  ModuleMeta
}

// ModuleMeta is the public-facing record of what was loaded. Source
// is intentionally not stored — only its sha256 — so the engine
// holds no copy of the rego text after PrepareForEval succeeds.
type ModuleMeta struct {
	Name       string
	Entrypoint string
	RuleHash   string // sha256(source-bytes), hex
}

// Option is the functional-options shape used by New.
type Option func(*Engine)

// WithTimeout overrides the default per-evaluation deadline. Values
// outside [50ms, 1s] are clamped to the bound and a warn is logged
// (guide §2.8: bounded timeout is the sandbox; making it
// arbitrary-valued reopens the DoS surface a malicious rule pack
// could exploit).
func WithTimeout(t time.Duration) Option {
	return func(e *Engine) {
		switch {
		case t < minTimeout:
			e.logger.Warn("rego.WithTimeout below floor; clamping",
				"requested", t, "applied", minTimeout)
			t = minTimeout
		case t > maxTimeout:
			e.logger.Warn("rego.WithTimeout above ceiling; clamping",
				"requested", t, "applied", maxTimeout)
			t = maxTimeout
		}
		e.defaultTimeout = t
	}
}

// WithLogger swaps the structured logger. Defaults to slog.Default()
// so the engine is wired up without extra ceremony in main.go and
// tests.
func WithLogger(l *slog.Logger) Option {
	return func(e *Engine) {
		if l != nil {
			e.logger = l
		}
	}
}

// New constructs an Engine with the given options. The logger field
// is initialized before any option runs, so WithTimeout can log its
// clamp warnings at construction time.
func New(opts ...Option) *Engine {
	e := &Engine{
		modules:        make(map[string]*compiledModule),
		logger:         slog.Default(),
		defaultTimeout: defaultTimeout,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// LoadModule compiles regoSrc and registers it under name. Replaces
// any module previously registered under that name, so reloads from
// the rule-pack loader (P2-T8) just call LoadModule again with the
// new bytes.
//
// entrypoint is the fully-qualified Rego query, e.g.
// "data.kubeatlas.openshift.route.derive". Caller is responsible for
// matching it to the module's package declaration; we surface OPA's
// compile error verbatim if they disagree.
func (e *Engine) LoadModule(ctx context.Context, name, regoSrc, entrypoint string) error {
	if name == "" || regoSrc == "" || entrypoint == "" {
		return errors.New("rego.LoadModule: name, source, entrypoint all required")
	}

	r := rego.New(
		rego.Query(entrypoint),
		rego.Module(name, regoSrc),
	)
	pq, err := r.PrepareForEval(ctx)
	if err != nil {
		return fmt.Errorf("rego.LoadModule %s: %w", name, err)
	}

	hash := sha256.Sum256([]byte(regoSrc))
	meta := ModuleMeta{
		Name:       name,
		Entrypoint: entrypoint,
		RuleHash:   hex.EncodeToString(hash[:]),
	}

	e.mu.Lock()
	e.modules[name] = &compiledModule{query: pq, meta: meta}
	e.mu.Unlock()

	e.logger.Info("rego module loaded",
		"name", name,
		"entrypoint", entrypoint,
		"rule_hash", meta.RuleHash[:12]) // first 12 chars suffice for grep
	return nil
}

// Evaluate runs the named module against input and returns the OPA
// result set. Wraps the call in evaluateWithGuards so a runaway rule
// or an OPA-internal panic surfaces as ErrEvalTimeout / ErrEvalPanic
// instead of stalling or crashing the caller.
//
// rego.ResultSet is leaked through the API on purpose: callers
// (P2-T8 loader, P2-T9 router/cache) decode result entries into
// edges, and forcing them into a kubeatlas-specific intermediate
// struct here would just be a translation layer with no value.
func (e *Engine) Evaluate(ctx context.Context, name string, input any) (rego.ResultSet, error) {
	e.mu.RLock()
	m, ok := e.modules[name]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("rego.Evaluate: module %q not loaded", name)
	}

	rs, err := evaluateWithGuards(ctx, m.query, input, e.defaultTimeout)
	switch {
	case err == nil:
		return rs, nil
	case errors.Is(err, ErrEvalTimeout):
		e.logger.Warn("rego evaluation timeout",
			"module", name,
			"rule_hash", m.meta.RuleHash[:12],
			"budget", e.defaultTimeout)
		return nil, err
	case errors.Is(err, ErrEvalPanic):
		e.logger.Error("rego evaluation panic",
			"module", name,
			"rule_hash", m.meta.RuleHash[:12],
			"err", err)
		return nil, err
	default:
		e.logger.Warn("rego evaluation error",
			"module", name,
			"rule_hash", m.meta.RuleHash[:12],
			"err", err)
		return nil, err
	}
}

// Loaded returns the metadata for every module currently registered.
// Used by health endpoints and tests to introspect engine state
// without a lock dance.
func (e *Engine) Loaded() []ModuleMeta {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]ModuleMeta, 0, len(e.modules))
	for _, m := range e.modules {
		out = append(out, m.meta)
	}
	return out
}

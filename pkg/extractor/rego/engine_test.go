// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-policy-agent/opa/v1/rego"
)

// helloModule is the smallest possible Rego v1 module: returns true
// when the input kind matches "Bad". Phase 2 uses Rego v1 syntax
// (`if` keyword required in rule bodies; `:=` for assignments).
const helloModule = `
package x

import rego.v1

default deny := false

deny if {
	input.kind == "Bad"
}
`

// loopModule is a deliberate runaway: numbers.range explodes the
// search space until the timeout fires. Without timeout + recover
// guards this would block the test goroutine indefinitely.
const loopModule = `
package x

import rego.v1

default deny := false

deny if {
	numbers.range(1, 100000000)[_] > 0
}
`

func TestEngine_LoadAndEvaluate_HappyPath(t *testing.T) {
	e := newSilentEngine(t)
	ctx := context.Background()

	if err := e.LoadModule(ctx, "x.rego", helloModule, "data.x.deny"); err != nil {
		t.Fatalf("LoadModule: %v", err)
	}

	// Negative input → deny == false.
	rs, err := e.Evaluate(ctx, "x.rego", map[string]any{"kind": "Pod"})
	if err != nil {
		t.Fatalf("Evaluate(Pod): %v", err)
	}
	if got := boolResult(t, rs); got != false {
		t.Errorf("Evaluate(Pod) deny = %v, want false", got)
	}

	// Positive input → deny == true.
	rs, err = e.Evaluate(ctx, "x.rego", map[string]any{"kind": "Bad"})
	if err != nil {
		t.Fatalf("Evaluate(Bad): %v", err)
	}
	if got := boolResult(t, rs); got != true {
		t.Errorf("Evaluate(Bad) deny = %v, want true", got)
	}
}

// TestEngine_Evaluate_Timeout asserts the loop rule returns a timeout
// error within the configured budget — never blocking. Budget is
// stretched to 200ms; the wall-clock observation must be under 500ms
// (gives slack for OPA cleanup) and the error must wrap
// ErrEvalTimeout.
func TestEngine_Evaluate_Timeout(t *testing.T) {
	e := newSilentEngine(t, WithTimeout(200*time.Millisecond))
	ctx := context.Background()
	if err := e.LoadModule(ctx, "loop.rego", loopModule, "data.x.deny"); err != nil {
		t.Fatalf("LoadModule: %v", err)
	}

	start := time.Now()
	_, err := e.Evaluate(ctx, "loop.rego", map[string]any{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrEvalTimeout) {
		t.Errorf("err = %v, want ErrEvalTimeout", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed %s exceeds 500ms slack budget for 200ms timeout", elapsed)
	}
	t.Logf("loop module aborted in %s", elapsed)
}

// TestRunGuarded_PanicRecover injects a panicking fn directly so the
// recover branch is unit-testable without forcing OPA to panic.
func TestRunGuarded_PanicRecover(t *testing.T) {
	_, err := runGuarded(context.Background(), 100*time.Millisecond,
		func(ctx context.Context) (rego.ResultSet, error) {
			panic("synthetic panic for test")
		})
	if err == nil {
		t.Fatal("expected recovered panic, got nil")
	}
	if !errors.Is(err, ErrEvalPanic) {
		t.Errorf("err = %v, want ErrEvalPanic", err)
	}
	if !strings.Contains(err.Error(), "synthetic panic") {
		t.Errorf("err %q does not include the panic message", err)
	}
}

// TestRunGuarded_NonTimeoutError covers the bare-error pass-through:
// non-deadline errors must surface unchanged so callers can diagnose.
func TestRunGuarded_NonTimeoutError(t *testing.T) {
	want := errors.New("synthetic non-timeout failure")
	_, err := runGuarded(context.Background(), 100*time.Millisecond,
		func(ctx context.Context) (rego.ResultSet, error) {
			return nil, want
		})
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
	if errors.Is(err, ErrEvalTimeout) || errors.Is(err, ErrEvalPanic) {
		t.Errorf("err %v should not match timeout/panic sentinels", err)
	}
}

// TestEngine_WithTimeoutClamp asserts the [50ms, 1s] hard bounds:
// requests outside the range are clamped and a warn is logged.
func TestEngine_WithTimeoutClamp(t *testing.T) {
	cases := []struct {
		name      string
		requested time.Duration
		want      time.Duration
		wantWarn  string
	}{
		{"below floor", 1 * time.Millisecond, 50 * time.Millisecond, "below floor"},
		{"in range", 250 * time.Millisecond, 250 * time.Millisecond, ""},
		{"above ceiling", 10 * time.Second, time.Second, "above ceiling"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			e := New(WithLogger(logger), WithTimeout(c.requested))
			if e.defaultTimeout != c.want {
				t.Errorf("defaultTimeout = %s, want %s", e.defaultTimeout, c.want)
			}
			if c.wantWarn != "" && !strings.Contains(buf.String(), c.wantWarn) {
				t.Errorf("expected warn containing %q, got %q", c.wantWarn, buf.String())
			}
			if c.wantWarn == "" && buf.Len() > 0 {
				t.Errorf("unexpected log output for in-range timeout: %q", buf.String())
			}
		})
	}
}

// TestEngine_EvaluateUnknownModule fails cleanly when the module name
// has not been loaded. The error message must include the missing
// module name so operators can diagnose typos.
func TestEngine_EvaluateUnknownModule(t *testing.T) {
	e := newSilentEngine(t)
	_, err := e.Evaluate(context.Background(), "nope.rego", nil)
	if err == nil {
		t.Fatal("expected error for unloaded module, got nil")
	}
	if !strings.Contains(err.Error(), "nope.rego") {
		t.Errorf("err %q should mention the module name", err)
	}
}

// TestEngine_LoadModule_Validation rejects empty inputs at the
// boundary rather than letting OPA produce a confusing parse error.
func TestEngine_LoadModule_Validation(t *testing.T) {
	e := newSilentEngine(t)
	ctx := context.Background()
	cases := []struct{ name, src, entrypoint string }{
		{"", helloModule, "data.x.deny"},
		{"x.rego", "", "data.x.deny"},
		{"x.rego", helloModule, ""},
	}
	for _, c := range cases {
		if err := e.LoadModule(ctx, c.name, c.src, c.entrypoint); err == nil {
			t.Errorf("LoadModule(%q,%q,%q): expected error, got nil",
				c.name, ellip(c.src), c.entrypoint)
		}
	}
}

// TestEngine_LoadModule_BadSyntax surfaces OPA's compile error
// verbatim — callers (rule-pack loader in P2-T8) need it to point
// contributors at the broken file.
func TestEngine_LoadModule_BadSyntax(t *testing.T) {
	e := newSilentEngine(t)
	err := e.LoadModule(context.Background(), "broken.rego", "this is not rego", "data.x.deny")
	if err == nil {
		t.Fatal("expected compile error, got nil")
	}
}

// TestEngine_Loaded returns the registered modules' metadata in the
// shape the health endpoint will consume.
func TestEngine_Loaded(t *testing.T) {
	e := newSilentEngine(t)
	ctx := context.Background()
	if err := e.LoadModule(ctx, "a.rego", helloModule, "data.x.deny"); err != nil {
		t.Fatal(err)
	}
	if err := e.LoadModule(ctx, "b.rego", helloModule, "data.x.deny"); err != nil {
		t.Fatal(err)
	}
	got := e.Loaded()
	if len(got) != 2 {
		t.Fatalf("Loaded len = %d, want 2", len(got))
	}
	for _, m := range got {
		if m.RuleHash == "" || len(m.RuleHash) != 64 {
			t.Errorf("RuleHash %q, want 64-char hex", m.RuleHash)
		}
	}
}

// TestEngine_ConcurrentEvaluate exercises the read-mostly hot path
// under -race: multiple goroutines hammer Evaluate against the same
// module while one keeps reloading the module to bump the writer
// path. The Engine must not data-race.
func TestEngine_ConcurrentEvaluate(t *testing.T) {
	e := newSilentEngine(t)
	ctx := context.Background()
	if err := e.LoadModule(ctx, "x.rego", helloModule, "data.x.deny"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Reload writer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = e.LoadModule(ctx, "x.rego", helloModule, "data.x.deny")
			}
		}
	}()
	// Evaluate readers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = e.Evaluate(ctx, "x.rego", map[string]any{"kind": "Pod"})
			}
		}()
	}

	// Run readers for a short window, then stop the writer.
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// newSilentEngine builds an engine whose logs are discarded so test
// output stays readable. Tests that need to inspect log output build
// their own engine with a captured logger.
func newSilentEngine(t *testing.T, opts ...Option) *Engine {
	t.Helper()
	silent := slog.New(slog.NewTextHandler(&discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	all := append([]Option{WithLogger(silent)}, opts...)
	return New(all...)
}

// boolResult plucks the single boolean expression out of a Rego v1
// query result set ("data.x.deny" returns one expression).
func boolResult(t *testing.T, rs rego.ResultSet) bool {
	t.Helper()
	if len(rs) == 0 || len(rs[0].Expressions) == 0 {
		t.Fatalf("empty result set")
	}
	v, ok := rs[0].Expressions[0].Value.(bool)
	if !ok {
		t.Fatalf("expression value %T is not bool", rs[0].Expressions[0].Value)
	}
	return v
}

// ellip clips long strings in test failure messages so the source
// of a broken module does not flood test output.
func ellip(s string) string {
	const max = 16
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// discardWriter sinks log output during tests. slog.NewTextHandler
// does not accept io.Discard pre-1.21; this trivial Writer matches.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

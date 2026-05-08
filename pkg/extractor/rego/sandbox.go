// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/open-policy-agent/opa/v1/rego"
)

// ErrEvalTimeout is returned when a rego evaluation exceeds the
// configured per-call deadline. Callers can errors.Is against it to
// distinguish "rule was too slow" from "rule errored / panicked".
var ErrEvalTimeout = errors.New("rego: evaluation timeout")

// ErrEvalPanic is returned when the OPA SDK panics inside an
// evaluation. The original panic value is wrapped in the error
// message; the goroutine survives because evaluateWithGuards installs
// a defer/recover (guide §2.8 — without this a single bad rule kills
// the whole server).
var ErrEvalPanic = errors.New("rego: evaluation panic")

// evaluateWithGuards runs query.Eval(input) under a context-scoped
// timeout and a panic recover. It is the single execution boundary
// every Engine.Evaluate caller crosses; the two guards must always
// be paired (anti-pattern #11).
func evaluateWithGuards(ctx context.Context, query rego.PreparedEvalQuery, input any, timeout time.Duration) (rego.ResultSet, error) {
	return runGuarded(ctx, timeout, func(ctx context.Context) (rego.ResultSet, error) {
		return query.Eval(ctx, rego.EvalInput(input))
	})
}

// runGuarded is the testable core of evaluateWithGuards: any
// (ctx, fn) shape that returns rego.ResultSet runs under the same
// timeout + recover envelope. Tests inject a deliberately-panicking
// fn here to exercise the panic-recovery branch without needing to
// trick the OPA SDK into panicking.
func runGuarded(ctx context.Context, timeout time.Duration, fn func(context.Context) (rego.ResultSet, error)) (rs rego.ResultSet, err error) {
	defer func() {
		if r := recover(); r != nil {
			rs = nil
			err = fmt.Errorf("%w: %v", ErrEvalPanic, r)
		}
	}()

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rs, err = fn(cctx)
	if err != nil {
		// OPA propagates the deadline as ctx.Err(); flag it so
		// callers can react (warn log + Prometheus counter,
		// see Engine.Evaluate).
		if cctxErr := cctx.Err(); errors.Is(cctxErr, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: budget %s", ErrEvalTimeout, timeout)
		}
		return nil, err
	}
	return rs, nil
}

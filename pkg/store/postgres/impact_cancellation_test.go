// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// pgx can surface a socket deadline error when cancellation races a write.
// Callers must still identify the request cancellation, without a partial graph.
func TestReadImpactRowsCancellationDuringQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded}
	tx := impactQueryFailure{cancel: cancel, err: transport}
	called := false
	err := readImpactRows(ctx, tx, impactResourcesSQL, "", 1, 1024, func([]byte) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled projection: error=%v partial=%v", err, called)
	}
}

func TestReadImpactRowsPreservesUncancelledDriverFailure(t *testing.T) {
	transport := errors.New("synthetic driver failure")
	err := readImpactRows(context.Background(), impactQueryFailure{err: transport}, impactResourcesSQL, "", 1, 1024, func([]byte) error { return nil })
	if !errors.Is(err, transport) || errors.Is(err, context.Canceled) {
		t.Fatalf("uncancelled driver failure changed: %v", err)
	}
}

type impactQueryFailure struct {
	pgx.Tx
	cancel context.CancelFunc
	err    error
}

func (t impactQueryFailure) Query(context.Context, string, ...any) (pgx.Rows, error) {
	if t.cancel != nil {
		t.cancel()
	}
	return nil, t.err
}

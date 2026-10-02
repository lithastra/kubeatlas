// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
)

func TestStore_HistoryBounds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping disposable PostgreSQL+AGE integration tests in -short mode")
	}
	h := StartPostgresWithAGE(t)
	ctx := context.Background()
	s, err := New(ctx, Config{DSN: h.ConnStr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	t.Run("shared contract", func(t *testing.T) {
		storetest.RunHistoryBounds(t, func(t *testing.T) graph.GraphStore {
			t.Helper()
			if err := s.truncateAll(ctx); err != nil {
				t.Fatal(err)
			}
			return s
		})
	})
	t.Run("SQL needs only timestamps scope and kind", func(t *testing.T) {
		if _, err := s.pool.Exec(ctx, `CREATE ROLE history_bounds_reader;
GRANT USAGE ON SCHEMA public TO history_bounds_reader;
GRANT SELECT (ts, cluster_id, kind) ON public.resource_events TO history_bounds_reader;
GRANT SELECT (ts, cluster_id) ON public.snapshot_meta TO history_bounds_reader`); err != nil {
			t.Fatal(err)
		}
		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE history_bounds_reader`); err != nil {
			t.Fatal(err)
		}
		var first, last, marker *time.Time
		if err := tx.QueryRow(ctx, historyBoundsSQL, "prod", time.Unix(1, 0), time.Now()).Scan(&first, &last, &marker); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT data FROM public.resource_events`); err == nil {
			t.Fatal("restricted role had payload access")
		}
	})
	t.Run("cancelled aggregate releases one-slot pool", func(t *testing.T) {
		observer := &historyReadObserver{queryStarted: make(chan struct{}, 1)}
		reader := newImpactTestReader(t, h.ConnStr, observer)
		lock, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Rollback(ctx) }()
		if _, err := lock.Exec(ctx, `LOCK TABLE public.snapshot_meta IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		q := graph.HistoryBoundsQuery{From: time.Unix(1, 0), To: time.Now()}
		bounded, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			got, err := reader.ReadHistoryBounds(bounded, q)
			if got != (graph.HistoryBounds{}) {
				err = errors.New("cancelled query returned partial bounds")
			}
			done <- err
		}()
		select {
		case <-observer.queryStarted:
			cancel()
		case <-time.After(3 * time.Second):
			t.Fatal("aggregate never reached database query")
		}
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("aggregate did not release connection")
		}
		if !observer.readOnly.Load() || !observer.timeout.Load() || observer.releases.Load() == 0 {
			t.Fatal("read-only transaction/timeout/cleanup missing")
		}
		if err := lock.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		reuse, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		if _, err := reader.ReadHistoryBounds(reuse, q); err != nil || reader.pool.Stat().AcquiredConns() != 0 {
			t.Fatalf("pool slot not reusable: %v", err)
		}
	})
}

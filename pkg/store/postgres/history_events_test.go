// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
)

func TestStore_HistoryEvents(t *testing.T) {
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
	reset := func(t *testing.T) {
		t.Helper()
		if err := s.truncateAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("shared contract", func(t *testing.T) {
		storetest.RunHistoryEvents(t, func(t *testing.T) graph.GraphStore { reset(t); return s })
	})
	t.Run("SQL cannot access payload column and enforces wire cap", func(t *testing.T) {
		reset(t)
		base := time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC)
		for _, name := range []string{"small", strings.Repeat("wire-canary", 1000)} {
			if err := s.AppendEvent(ctx, graph.ResourceEvent{Timestamp: base, ClusterID: "prod", Kind: "Pod", Name: name, EventType: graph.EventTypeAdd}); err != nil {
				t.Fatal(err)
			}
		}
		// Test-only restricted role in the disposable database. The application
		// read must work without SELECT privilege on Data, not merely strip it
		// from JSON after transferring it out of PostgreSQL.
		if _, err := s.pool.Exec(ctx, `CREATE ROLE history_metadata_reader;
GRANT USAGE ON SCHEMA public TO history_metadata_reader;
GRANT SELECT (id,ts,cluster_id,namespace,kind,uid,name,event_type,resource_version)
ON public.resource_events TO history_metadata_reader`); err != nil {
			t.Fatal(err)
		}
		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE history_metadata_reader`); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, historyEventsSQL, "prod", "", base, base.Add(time.Hour), 3, 1024)
		if err != nil {
			t.Fatal(err)
		}
		var bodies [][]byte
		for rows.Next() {
			var body []byte
			if err := rows.Scan(&body); err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, body)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(bodies) != 2 || !strings.Contains(string(bodies[0]), "small") || strings.Contains(string(bodies[0]), `"data"`) || bodies[1] != nil {
			t.Fatal("SQL row projection failed metadata allowlist/wire cap")
		}
		if _, err := tx.Exec(ctx, `SELECT data FROM public.resource_events`); err == nil {
			t.Fatal("restricted role unexpectedly has payload access")
		}
	})
	t.Run("query cancellation releases the transaction and pool slot", func(t *testing.T) {
		reset(t)
		observer := &historyReadObserver{queryStarted: make(chan struct{}, 1)}
		reader := newImpactTestReader(t, h.ConnStr, observer) // Isolated one-slot pool.
		lock, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Rollback(ctx) }()
		if _, err := lock.Exec(ctx, `LOCK TABLE public.resource_events IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		q := graph.HistoryEventQuery{From: time.Unix(1, 0), To: time.Now()}
		bounded, cancel := context.WithCancel(ctx)
		defer cancel()
		finished := make(chan error, 1)
		go func() {
			page, err := reader.ReadHistoryEvents(bounded, q)
			if page.Events != nil || page.Truncated {
				err = errors.New("cancelled query returned partial metadata")
			}
			finished <- err
		}()
		// Cancel at the query boundary rather than assuming cold connection setup
		// finishes within a tiny timeout on every contributor's machine.
		select {
		case <-observer.queryStarted:
			cancel()
		case <-time.After(3 * time.Second):
			t.Fatal("history read never reached the event query")
		}
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled query error=%v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled query did not release its connection within the bound")
		}
		if !observer.started.Load() || !observer.readOnly.Load() || !observer.timeout.Load() || observer.releases.Load() == 0 {
			t.Fatal("query did not establish read-only timeout safeguards or release its connection")
		}
		if err := lock.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		// pgx/puddle destroys cancelled connections asynchronously; the acquired
		// gauge can lag Release. Require bounded reuse of the actual ONE-slot
		// pool, not an immediate gauge value or a spare connection masking a leak.
		reuse, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		if _, err := reader.ReadHistoryEvents(reuse, q); err != nil {
			t.Fatal(err)
		}
		if reader.pool.Stat().AcquiredConns() != 0 {
			t.Fatal("history read retained a pool slot after successful reuse")
		}
	})
	t.Run("pool acquisition obeys caller cancellation", func(t *testing.T) {
		one, err := New(ctx, Config{DSN: h.ConnStr, MaxConns: 1})
		if err != nil {
			t.Fatal(err)
		}
		defer one.Close()
		held, err := one.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Release()
		bounded, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		page, err := one.ReadHistoryEvents(bounded, graph.HistoryEventQuery{From: time.Unix(1, 0), To: time.Now()})
		if !errors.Is(err, context.DeadlineExceeded) || page.Events != nil || page.Truncated {
			t.Fatal("pool wait ignored deadline or returned partial data")
		}
	})
}

type historyReadObserver struct {
	queryStarted chan struct{}
	started      atomic.Bool
	readOnly     atomic.Bool
	timeout      atomic.Bool
	releases     atomic.Int64
}

func (o *historyReadObserver) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == historyEventsSQL || data.SQL == historyBoundsSQL {
		o.started.Store(true)
		select {
		case o.queryStarted <- struct{}{}:
		default:
		}
	}
	if strings.HasPrefix(data.SQL, "begin") && strings.Contains(data.SQL, "read only") {
		o.readOnly.Store(true)
	}
	if data.SQL == `SET LOCAL statement_timeout = '5s'` {
		o.timeout.Store(true)
	}
	return ctx
}

func (*historyReadObserver) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (o *historyReadObserver) TraceRelease(*pgxpool.Pool, pgxpool.TraceReleaseData) {
	o.releases.Add(1)
}

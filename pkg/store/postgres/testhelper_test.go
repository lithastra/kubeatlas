// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// AGEHandle is the value returned by StartPostgresWithAGE. ConnStr is
// the external connection string (sslmode=disable, suitable for pgx
// or psql); Container is exposed so tests may run psql via Exec
// without pulling in a Go pg driver — pgx lands in P2-T2.
type AGEHandle struct {
	Container *postgres.PostgresContainer
	ConnStr   string
}

// StartPostgresWithAGE boots a single-use Postgres container preloaded
// with the Apache AGE extension, runs CREATE EXTENSION IF NOT EXISTS
// age in the default database, and registers container termination
// via t.Cleanup so it runs even if the test panics.
//
// LOAD 'age' is session-scoped in AGE, so callers must still issue it
// (and SET search_path = ag_catalog, "$user", public) on every new
// connection. That is the responsibility of the production Store, not
// this helper.
//
// Image: apache/age:release_PG16_1.6.0 — pinned (no :latest, no
// _latest aliases; see guide anti-pattern #26). Apache AGE publishes
// tags as release_PG<major>_<age-version>; the "PG14_latest" form
// the guide draft references does not exist on Docker Hub. PG16 +
// AGE 1.6 is the newest stable combination as of v1.0 GA.
func StartPostgresWithAGE(t *testing.T) AGEHandle {
	t.Helper()

	ctx := context.Background()
	start := time.Now()

	container, err := postgres.Run(ctx,
		"apache/age:release_PG16_1.6.0",
		postgres.WithDatabase("kubeatlas"),
		postgres.WithUsername("kubeatlas"),
		postgres.WithPassword("kubeatlas"),
		testcontainers.WithWaitStrategy(
			// PG starts twice during initdb; wait for the second
			// "ready to accept connections" log to avoid races
			// where the helper connects during the bootstrap pass.
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("StartPostgresWithAGE: run container: %v", err)
	}

	// Register cleanup before any further calls so that a failure
	// below still tears the container down.
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("StartPostgresWithAGE: terminate: %v", err)
		}
	})

	if err := execPSQL(ctx, container, "CREATE EXTENSION IF NOT EXISTS age"); err != nil {
		t.Fatalf("StartPostgresWithAGE: create AGE extension: %v", err)
	}

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("StartPostgresWithAGE: connection string: %v", err)
	}

	t.Logf("StartPostgresWithAGE ready in %s", time.Since(start).Round(time.Millisecond))

	return AGEHandle{Container: container, ConnStr: connStr}
}

// execPSQL runs sql in the container's bundled psql against the
// kubeatlas database as the kubeatlas user. The whole command runs in
// one session, so multi-statement scripts that need LOAD 'age' work.
func execPSQL(ctx context.Context, c *postgres.PostgresContainer, sql string) error {
	code, reader, err := c.Exec(ctx, []string{
		"psql",
		"-U", "kubeatlas",
		"-d", "kubeatlas",
		"-v", "ON_ERROR_STOP=1",
		"-tAc", sql,
	})
	if err != nil {
		return err
	}
	out, _ := io.ReadAll(reader)
	if code != 0 {
		return &psqlError{exitCode: code, output: strings.TrimSpace(string(out))}
	}
	return nil
}

type psqlError struct {
	exitCode int
	output   string
}

func (e *psqlError) Error() string {
	return "psql exit " + itoa(e.exitCode) + ": " + e.output
}

// itoa avoids strconv just to keep imports minimal in a helper file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 4)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}

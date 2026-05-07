// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// currentSchemaVersion is the highest migration this binary knows
// how to apply. Bumping it requires a matching migrate/NNN_*.sql
// file shipped in the same commit; ADRs guard breaking changes
// (guide §2.1: GraphStore interface frozen).
const currentSchemaVersion = 1

//go:embed migrate/*.sql
var migrationFS embed.FS

// migration is one ordered, atomic step from version-1 to version.
type migration struct {
	Version int
	Name    string
	SQL     string
}

// migrationFilename matches "001_initial.sql" / "012_add_rbac.sql".
var migrationFilename = regexp.MustCompile(`^(\d{3})_([a-z0-9_]+)\.sql$`)

// loadMigrations parses every SQL file under migrate/ in the
// embedded FS into an ordered slice. Thin wrapper kept for callers
// that don't need to override the source.
func loadMigrations() ([]migration, error) {
	return loadMigrationsFrom(migrationFS, "migrate")
}

// loadMigrationsFrom is the testable core: given any fs.FS, return
// the migrations under dir. Filenames must be NNN_name.sql and the
// resulting version sequence must be 1..N with no gaps.
func loadMigrationsFrom(srcFS fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(srcFS, dir)
	if err != nil {
		return nil, fmt.Errorf("loadMigrations: read %s: %w", dir, err)
	}
	var ms []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFilename.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("loadMigrations: bad filename %q (want NNN_name.sql)", e.Name())
		}
		ver, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("loadMigrations: bad version in %q: %w", e.Name(), err)
		}
		body, err := fs.ReadFile(srcFS, dir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("loadMigrations: read %s: %w", e.Name(), err)
		}
		ms = append(ms, migration{Version: ver, Name: m[2], SQL: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })

	// Reject gaps and duplicates so a renumber typo cannot silently
	// skip a migration.
	for i, m := range ms {
		if m.Version != i+1 {
			return nil, fmt.Errorf("loadMigrations: migration %d at index %d (gap or duplicate)", m.Version, i)
		}
	}
	return ms, nil
}

// migrate brings the database from its current schema_migrations
// version to currentSchemaVersion. Each migration runs in its own
// transaction; a failure leaves prior versions applied so a re-run
// resumes exactly where it stopped.
func (s *Store) migrate(ctx context.Context) error {
	if err := s.ensureMigrationTable(ctx); err != nil {
		return err
	}
	current, err := s.currentVersion(ctx)
	if err != nil {
		return err
	}
	if current > currentSchemaVersion {
		return fmt.Errorf(
			"postgres.migrate: db at version %d, binary supports up to %d (downgrade not supported)",
			current, currentSchemaVersion,
		)
	}

	all, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range all {
		if m.Version <= current {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// ensureMigrationTable creates schema_migrations if absent. Kept
// outside the migration mechanism itself so a fresh database can
// record version-1 having been applied.
func (s *Store) ensureMigrationTable(ctx context.Context) error {
	const ddl = `
		CREATE TABLE IF NOT EXISTS public.schema_migrations (
			version    INT PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`
	if _, err := s.pool.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("postgres.migrate: create schema_migrations: %w", err)
	}
	return nil
}

// currentVersion returns the highest applied migration version, or 0
// if schema_migrations is empty.
func (s *Store) currentVersion(ctx context.Context) (int, error) {
	var v *int
	err := s.pool.QueryRow(ctx, `SELECT MAX(version) FROM public.schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("postgres.migrate: read version: %w", err)
	}
	if v == nil {
		return 0, nil
	}
	return *v, nil
}

// applyMigration runs a single migration in its own transaction.
// AGE setup statements (LOAD 'age', SET search_path) are session
// scoped, so the whole script runs on one connection; pgx.Tx wraps
// a single connection by design, which gives us that for free.
func (s *Store) applyMigration(ctx context.Context, m migration) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("postgres.migrate: acquire conn for v%d: %w", m.Version, err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres.migrate: begin v%d: %w", m.Version, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("postgres.migrate: apply v%d (%s): %w", m.Version, m.Name, err)
	}
	// schema_migrations lives in the public schema. The migration
	// SQL above may have issued SET search_path = ag_catalog, ...
	// (AGE convention) which persists for the rest of this tx, so
	// fully-qualify the bookkeeping INSERT to be search_path-immune.
	if _, err := tx.Exec(ctx,
		`INSERT INTO public.schema_migrations (version, name) VALUES ($1, $2)`,
		m.Version, m.Name,
	); err != nil {
		return fmt.Errorf("postgres.migrate: record v%d: %w", m.Version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres.migrate: commit v%d: %w", m.Version, err)
	}
	return nil
}

// graphLabels returns the (vertex, edge) label name pairs registered
// for the kubeatlas graph. Used by tests and operators to verify
// migration outcome.
func (s *Store) graphLabels(ctx context.Context) (vertices, edges []string, err error) {
	// l.kind is the AGE single-byte char type (OID 18); cast to text
	// so pgx's binary protocol can decode it into a Go string.
	const sql = `
		SELECT l.name, l.kind::text
		FROM ag_catalog.ag_label l
		JOIN ag_catalog.ag_graph g ON l.graph = g.graphid
		WHERE g.name = 'kubeatlas'
		ORDER BY l.name
	`
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, nil, fmt.Errorf("graphLabels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, nil, fmt.Errorf("graphLabels: scan: %w", err)
		}
		// AGE seeds two implicit labels per graph (_ag_label_vertex /
		// _ag_label_edge); skip them so callers see only the labels
		// the migration created.
		if strings.HasPrefix(name, "_ag_label") {
			continue
		}
		switch kind {
		case "v":
			vertices = append(vertices, name)
		case "e":
			edges = append(edges, name)
		}
	}
	return vertices, edges, rows.Err()
}

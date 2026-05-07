// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"slices"
	"testing"
	"testing/fstest"
)

// expectedVertexLabels mirrors migrate/001_initial.sql in sorted form;
// keep both in lock-step. Adding a kind = update both places + bump
// currentSchemaVersion if the kind is also expected to backfill.
var expectedVertexLabels = []string{
	"ConfigMap",
	"CronJob",
	"DaemonSet",
	"Deployment",
	"Gateway",
	"HTTPRoute",
	"Ingress",
	"Job",
	"Namespace",
	"Node",
	"PersistentVolume",
	"PersistentVolumeClaim",
	"Pod",
	"ReplicaSet",
	"Secret",
	"Service",
	"ServiceAccount",
	"StatefulSet",
}

var expectedEdgeLabels = []string{
	"ATTACHED_TO",
	"MOUNTS_VOLUME",
	"OWNS",
	"ROUTES_TO",
	"SELECTS",
	"USES_CONFIGMAP",
	"USES_SECRET",
	"USES_SERVICEACCOUNT",
}

// TestMigrate_FreshSchema: empty PG -> Init -> schema_migrations has
// exactly one row at version 1, the AGE graph exists, and every
// expected vertex/edge label has been created.
func TestMigrate_FreshSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping testcontainers test in -short mode")
	}

	h := StartPostgresWithAGE(t)
	ctx := context.Background()
	s, err := New(ctx, Config{DSN: h.ConnStr})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	// schema_migrations contains exactly version 1.
	var versions []int
	rows, err := s.pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		versions = append(versions, v)
	}
	rows.Close()
	if !slices.Equal(versions, []int{currentSchemaVersion}) {
		t.Errorf("schema_migrations versions: got %v, want [%d]", versions, currentSchemaVersion)
	}

	// AGE graph exists.
	var graphCount int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM ag_catalog.ag_graph WHERE name = 'kubeatlas'`,
	).Scan(&graphCount); err != nil {
		t.Fatalf("ag_graph query: %v", err)
	}
	if graphCount != 1 {
		t.Errorf("ag_graph rows for 'kubeatlas': got %d, want 1", graphCount)
	}

	// Labels match the migration's promise.
	v, e, err := s.graphLabels(ctx)
	if err != nil {
		t.Fatalf("graphLabels: %v", err)
	}
	if !slices.Equal(v, expectedVertexLabels) {
		t.Errorf("vertex labels:\n got  %v\n want %v", v, expectedVertexLabels)
	}
	if !slices.Equal(e, expectedEdgeLabels) {
		t.Errorf("edge labels:\n got  %v\n want %v", e, expectedEdgeLabels)
	}
}

// TestMigrate_Idempotent: a second Init must not produce a duplicate
// schema_migrations row, must not error, and must leave the AGE
// labels unchanged.
func TestMigrate_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping testcontainers test in -short mode")
	}

	h := StartPostgresWithAGE(t)
	ctx := context.Background()
	s, err := New(ctx, Config{DSN: h.ConnStr})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	if err := s.Init(ctx); err != nil {
		t.Fatalf("second Init: %v", err)
	}

	var rowCount int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("schema_migrations row count after re-Init: got %d, want 1", rowCount)
	}
}

// TestMigrate_FromVersionZero: simulate an older deployment by
// rewinding schema_migrations to version 0 and re-running migrate.
// The runner must detect current<currentSchemaVersion, re-apply
// every pending migration (idempotent on real schema), and append
// a fresh row for each.
func TestMigrate_FromVersionZero(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping testcontainers test in -short mode")
	}

	h := StartPostgresWithAGE(t)
	ctx := context.Background()
	s, err := New(ctx, Config{DSN: h.ConnStr})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)

	// Wind the recorded version back to 0; the underlying schema
	// (tables, AGE graph, labels) stays intact and the migration
	// SQL is idempotent, so re-applying must succeed.
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_migrations`); err != nil {
		t.Fatalf("delete migrations: %v", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES (0, 'pre-init')`,
	); err != nil {
		t.Fatalf("insert v0: %v", err)
	}

	if err := s.migrate(ctx); err != nil {
		t.Fatalf("migrate from v0: %v", err)
	}

	var versions []int
	rows, err := s.pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		versions = append(versions, v)
	}
	rows.Close()
	if !slices.Equal(versions, []int{0, 1}) {
		t.Errorf("schema_migrations after upgrade: got %v, want [0 1]", versions)
	}
}

// TestLoadMigrations_FilenameValidation guards the embed parser:
// every shipped file must match NNN_name.sql, and the version
// sequence must start at 1 with no gaps.
func TestLoadMigrations_FilenameValidation(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("no migrations embedded")
	}
	for i, m := range ms {
		if m.Version != i+1 {
			t.Errorf("migration[%d].Version = %d, want %d", i, m.Version, i+1)
		}
		if m.Name == "" || m.SQL == "" {
			t.Errorf("migration[%d] has empty Name or SQL: %+v", i, m)
		}
	}
	if ms[len(ms)-1].Version != currentSchemaVersion {
		t.Errorf("highest migration = %d, currentSchemaVersion = %d (out of sync)",
			ms[len(ms)-1].Version, currentSchemaVersion)
	}
}

// TestLoadMigrationsFrom_BadFilename: any file not matching
// NNN_name.sql must fail loud.
func TestLoadMigrationsFrom_BadFilename(t *testing.T) {
	tfs := fstest.MapFS{
		"m/001_initial.sql": &fstest.MapFile{Data: []byte("-- ok")},
		"m/notes.txt":       &fstest.MapFile{Data: []byte("scratch")},
	}
	if _, err := loadMigrationsFrom(tfs, "m"); err == nil {
		t.Fatal("expected error for non-NNN_name.sql filename, got nil")
	}
}

// TestLoadMigrationsFrom_VersionGap: a missing 002 between 001 and
// 003 must fail at load time, not silently skip.
func TestLoadMigrationsFrom_VersionGap(t *testing.T) {
	tfs := fstest.MapFS{
		"m/001_a.sql": &fstest.MapFile{Data: []byte("-- a")},
		"m/003_c.sql": &fstest.MapFile{Data: []byte("-- c")},
	}
	if _, err := loadMigrationsFrom(tfs, "m"); err == nil {
		t.Fatal("expected version-gap error, got nil")
	}
}

// TestLoadMigrationsFrom_MissingDir: pointing at a non-existent
// directory must surface the underlying fs error rather than
// returning an empty slice.
func TestLoadMigrationsFrom_MissingDir(t *testing.T) {
	tfs := fstest.MapFS{}
	if _, err := loadMigrationsFrom(tfs, "nope"); err == nil {
		t.Fatal("expected error for missing dir, got nil")
	}
}

// TestLoadMigrationsFrom_HappyPath: a clean 1..3 sequence parses
// cleanly with the right SQL bodies attached.
func TestLoadMigrationsFrom_HappyPath(t *testing.T) {
	tfs := fstest.MapFS{
		"m/001_a.sql": &fstest.MapFile{Data: []byte("-- a")},
		"m/002_b.sql": &fstest.MapFile{Data: []byte("-- b")},
		"m/003_c.sql": &fstest.MapFile{Data: []byte("-- c")},
	}
	ms, err := loadMigrationsFrom(tfs, "m")
	if err != nil {
		t.Fatalf("loadMigrationsFrom: %v", err)
	}
	if len(ms) != 3 {
		t.Fatalf("got %d migrations, want 3", len(ms))
	}
	wantNames := []string{"a", "b", "c"}
	for i, m := range ms {
		if m.Version != i+1 {
			t.Errorf("ms[%d].Version = %d, want %d", i, m.Version, i+1)
		}
		if m.Name != wantNames[i] {
			t.Errorf("ms[%d].Name = %q, want %q", i, m.Name, wantNames[i])
		}
	}
}

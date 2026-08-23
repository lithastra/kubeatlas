// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// expectedVertexLabels mirrors the AGE vertex labels created across
// migrate/001_initial.sql (core kinds) and migrate/004_networkpolicy
// _labels.sql (NetworkPolicy), in sorted form; keep this list in
// lock-step. Adding a kind = update both places + bump
// currentSchemaVersion if the kind is also expected to backfill.
var expectedVertexLabels = []string{
	"ClusterRole",
	"ClusterRoleBinding",
	"ConfigMap",
	"CronJob",
	"DaemonSet",
	"Deployment",
	"Gateway",
	"HTTPRoute",
	"Ingress",
	"Job",
	"Namespace",
	"NetworkPolicy", // migrate/004 (P3-T1, F-109)
	"Node",
	"PersistentVolume",
	"PersistentVolumeClaim",
	"Pod",
	"ReplicaSet",
	"Role",
	"RoleBinding",
	"Secret",
	"Service",
	"ServiceAccount",
	"StatefulSet",
}

// expectedEdgeLabels mirrors the AGE edge labels created across
// migrate/001_initial.sql (8 Phase 0 types), migrate/002_rbac_labels
// .sql (BINDS_SUBJECT / BINDS_ROLE) and migrate/004_networkpolicy
// _labels.sql (SELECTS_NP / ALLOWS_FROM / ALLOWS_TO), sorted.
var expectedEdgeLabels = []string{
	"ALLOWS_FROM", // migrate/004 (P3-T1, F-109)
	"ALLOWS_TO",   // migrate/004 (P3-T1, F-109)
	"ATTACHED_TO",
	"BINDS_ROLE",
	"BINDS_SUBJECT",
	"MOUNTS_VOLUME",
	"OWNS",
	"ROUTES_TO",
	"SELECTS",
	"SELECTS_NP", // migrate/004 (P3-T1, F-109)
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
	wantVersions := make([]int, currentSchemaVersion)
	for i := range wantVersions {
		wantVersions[i] = i + 1
	}
	if !slices.Equal(versions, wantVersions) {
		t.Errorf("schema_migrations versions: got %v, want %v", versions, wantVersions)
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
	if rowCount != currentSchemaVersion {
		t.Errorf("schema_migrations row count after re-Init: got %d, want %d",
			rowCount, currentSchemaVersion)
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
	want := []int{0}
	for i := 1; i <= currentSchemaVersion; i++ {
		want = append(want, i)
	}
	if !slices.Equal(versions, want) {
		t.Errorf("schema_migrations after upgrade: got %v, want %v", versions, want)
	}
}

// TestMigrate_V11ScrubsExistingSecretAndEventPayloads simulates a v1.5.1
// database at schema v10. The v11 migration must erase the canary before the
// store becomes ready and install constraints that prevent reintroduction.
func TestMigrate_V11ScrubsExistingSecretAndEventPayloads(t *testing.T) {
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

	// Rewind only v11 and remove its constraints so we can seed the exact
	// unsafe shape an older binary wrote.
	if _, err := s.pool.Exec(ctx, `
		ALTER TABLE public.resources DROP CONSTRAINT resources_secret_reference_only;
		ALTER TABLE public.resource_events DROP CONSTRAINT resource_events_metadata_only;
		DELETE FROM public.schema_migrations WHERE version = 11;
	`); err != nil {
		t.Fatalf("rewind v11: %v", err)
	}
	const canary = "kubeatlas-v151-secret-canary"
	dep := graph.Resource{Kind: "Deployment", Namespace: "demo", Name: "api"}
	secret := graph.SecretReferenceResource("demo", "database", "")
	if err := s.UpsertResource(ctx, dep); err != nil {
		t.Fatalf("seed Deployment: %v", err)
	}
	if err := s.UpsertResource(ctx, secret); err != nil {
		t.Fatalf("seed Secret vertex: %v", err)
	}
	if err := s.UpsertEdge(ctx, graph.Edge{From: dep.ID(), To: secret.ID(), Type: graph.EdgeTypeUsesSecret}); err != nil {
		t.Fatalf("seed incoming Secret edge: %v", err)
	}
	if err := s.UpsertEdge(ctx, graph.Edge{From: secret.ID(), To: dep.ID(), Type: graph.EdgeTypeOwns}); err != nil {
		t.Fatalf("seed legacy outgoing Secret edge: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE public.resources SET data = jsonb_build_object(
				'kind', 'Secret', 'name', 'database', 'namespace', 'demo',
				'labels', jsonb_build_object('team', 'platform'),
				'raw', jsonb_build_object(
					'kind', 'Secret',
					'data', jsonb_build_object('password', $1::text),
					'stringData', jsonb_build_object('token', $1::text),
					'metadata', jsonb_build_object('annotations', jsonb_build_object(
						'kubectl.kubernetes.io/last-applied-configuration', $1::text
					))
				)
			)
		WHERE id = 'demo/Secret/database'
	`, canary); err != nil {
		t.Fatalf("seed v1.5.1 Secret: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO public.resource_events
			(namespace, kind, name, event_type, data)
		VALUES ('demo', 'Secret', 'database', 'update', jsonb_build_object('canary', $1::text))
	`, canary); err != nil {
		t.Fatalf("seed v1.5.1 event: %v", err)
	}

	if err := s.migrate(ctx); err != nil {
		t.Fatalf("migrate v10 to v11: %v", err)
	}

	var secretText string
	if err := s.pool.QueryRow(ctx,
		`SELECT data::text FROM public.resources WHERE id = 'demo/Secret/database'`,
	).Scan(&secretText); err != nil {
		t.Fatalf("read migrated Secret: %v", err)
	}
	if strings.Contains(secretText, canary) {
		t.Fatalf("migrated Secret still contains canary: %s", secretText)
	}
	if !strings.Contains(secretText, graph.ReferenceOnlyAnnotation) {
		t.Fatalf("migrated Secret is not marked reference-only: %s", secretText)
	}
	var eventPayloads int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM public.resource_events WHERE data IS NOT NULL`,
	).Scan(&eventPayloads); err != nil {
		t.Fatalf("count event payloads: %v", err)
	}
	if eventPayloads != 0 {
		t.Fatalf("resource_events has %d non-null payloads after migration", eventPayloads)
	}
	var secretEdges int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM public.edges
		WHERE from_id = 'demo/Secret/database' OR to_id = 'demo/Secret/database'
	`).Scan(&secretEdges); err != nil {
		t.Fatalf("count migrated Secret edges: %v", err)
	}
	if secretEdges != 0 {
		t.Fatalf("migration retained %d Secret incident edges", secretEdges)
	}
	if edges, err := s.ListEdges(ctx, dep.ID(), graph.DirectionOutgoing); err != nil {
		t.Fatalf("list AGE edges after migration: %v", err)
	} else if len(edges) != 0 {
		t.Fatalf("AGE retained Secret edges after migration: %v", edges)
	}

	// Initial informer resync recreates only the current incoming reference.
	if err := s.UpsertEdge(ctx, graph.Edge{From: dep.ID(), To: secret.ID(), Type: graph.EdgeTypeUsesSecret}); err != nil {
		t.Fatalf("recreate current Secret reference after migration: %v", err)
	}
	if edges, err := s.ListEdges(ctx, dep.ID(), graph.DirectionOutgoing); err != nil {
		t.Fatalf("list AGE edges after resync: %v", err)
	} else if len(edges) != 1 || edges[0].To != secret.ID() {
		t.Fatalf("resync edges = %v, want current Secret reference", edges)
	}

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO public.resource_events (namespace, kind, name, event_type, data)
		VALUES ('demo', 'ConfigMap', 'unsafe', 'add', '{"canary":true}'::jsonb)
	`); err == nil {
		t.Fatal("metadata-only constraint accepted a new event payload")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO public.resources (id, data) VALUES (
			'demo/Secret/unsafe',
			jsonb_build_object(
				'kind', 'Secret', 'name', 'unsafe', 'namespace', 'demo',
				'annotations', jsonb_build_object('kubeatlas.io/reference-only', 'true'),
				'data', jsonb_build_object('password', 'constraint-canary')
			)
		)
	`); err == nil {
		t.Fatal("reference-only constraint accepted an extra Secret payload field")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO public.resources (id, data) VALUES (
			'demo/Secret/missing-kind',
			jsonb_build_object(
				'name', 'missing-kind', 'namespace', 'demo',
				'annotations', jsonb_build_object('kubeatlas.io/reference-only', 'true'),
				'raw', jsonb_build_object('data', jsonb_build_object('password', 'constraint-canary'))
			)
		)
	`); err == nil {
		t.Fatal("reference-only constraint accepted a Secret-shaped ID with an omitted kind")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO public.resources (id, data) VALUES (
			'demo/Secret/wrong-name',
			jsonb_build_object(
				'kind', 'Secret', 'name', 'different-name', 'namespace', 'demo',
				'annotations', jsonb_build_object('kubeatlas.io/reference-only', 'true')
			)
		)
	`); err == nil {
		t.Fatal("reference-only constraint accepted identity fields that disagree with the row ID")
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

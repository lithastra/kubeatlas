// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
)

func TestStore_ImpactSnapshot(t *testing.T) {
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
		storetest.RunImpactSnapshot(t, func(t *testing.T) graph.GraphStore { reset(t); return s })
	})
	t.Run("reference allowlist before wire transfer", func(t *testing.T) {
		for _, schema := range []struct{ kind, version string }{
			{"Pod", "v1"}, {"Deployment", "apps/v1"}, {"CronJob", "batch/v1"},
		} {
			t.Run(schema.kind, func(t *testing.T) {
				reset(t)
				source, expected := storetest.ImpactReferenceFixture(t, schema.kind, schema.version)
				if err := s.UpsertResource(ctx, source); err != nil {
					t.Fatal(err)
				}
				var body []byte
				if err := s.pool.QueryRow(ctx, impactResourcesSQL, "prod", 2, graph.DefaultImpactBytes).Scan(&body); err != nil {
					t.Fatal(err)
				}
				var wire struct {
					ReferenceFields map[string]any `json:"referenceFields"`
				}
				if err := json.Unmarshal(body, &wire); err != nil || strings.Contains(string(body), "canary") || !reflect.DeepEqual(wire.ReferenceFields, expected.Raw) {
					t.Fatalf("SQL transferred fields outside the independent allowlist: %v", err)
				}
			})
		}
	})
	t.Run("malformed source fields cannot transfer nested payloads", func(t *testing.T) {
		reset(t)
		source, _ := storetest.ImpactReferenceFixture(t, "Pod", "v1")
		spec := source.Raw["spec"].(map[string]any)
		containers := spec["containers"].([]any)
		container := containers[0].(map[string]any)
		refs := container["envFrom"].([]any)
		refs[0].(map[string]any)["configMapRef"].(map[string]any)["optional"] = map[string]any{"password": "nested-canary"}
		mounts := container["volumeMounts"].([]any)
		mounts[0].(map[string]any)["subPath"] = map[string]any{"value": "path-canary"}
		source.Raw["metadata"].(map[string]any)["name"] = map[string]any{"payload": "metadata-canary"}
		spec["initContainers"] = map[string]any{"payload": "array-canary"}
		spec["ephemeralContainers"] = []any{"item-canary", nil, map[string]any{"ignored": "ignored-canary"}}
		if err := s.UpsertResource(ctx, source); err != nil {
			t.Fatal(err)
		}
		var body []byte
		if err := s.pool.QueryRow(ctx, impactResourcesSQL, "prod", 2, graph.DefaultImpactBytes).Scan(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "canary") {
			t.Fatal("malformed payload crossed wire")
		}
		var wire struct {
			ReferenceFields map[string]any `json:"referenceFields"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		builder, err := graph.NewImpactProjectionBuilder(graph.ImpactSnapshotOptions{ClusterID: "prod"})
		if err != nil {
			t.Fatal(err)
		}
		if err := builder.AddResource(ctx, source); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(wire.ReferenceFields, builder.Graph.Resources[0].Raw) {
			t.Fatal("SQL and Go disagree on invalid/null evidence")
		}
	})
	t.Run("wire budget includes references but excludes literal environment data", func(t *testing.T) {
		reset(t)
		source, _ := storetest.ImpactReferenceFixture(t, "Pod", "v1")
		containers := source.Raw["spec"].(map[string]any)["containers"].([]any)
		env := containers[0].(map[string]any)["env"].([]any)
		env[0].(map[string]any)["value"] = strings.Repeat("literal-canary", 30_000)
		if err := s.UpsertResource(ctx, source); err != nil {
			t.Fatal(err)
		}
		var body []byte
		if err := s.pool.QueryRow(ctx, impactResourcesSQL, "prod", 2, 65536).Scan(&body); err != nil || body == nil || strings.Contains(string(body), "canary") {
			t.Fatalf("literal env reached projection: %v", err)
		}
		if _, err := s.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{ClusterID: "prod", MaxBytes: 65536}); err != nil {
			t.Fatal(err)
		}
		// An allowlisted name must, in contrast, consume the SQL and Go budget.
		refs := containers[0].(map[string]any)["envFrom"].([]any)
		refs[0].(map[string]any)["configMapRef"].(map[string]any)["name"] = strings.Repeat("x", 70000)
		if err := s.UpsertResource(ctx, source); err != nil {
			t.Fatal(err)
		}
		if err := s.pool.QueryRow(ctx, impactResourcesSQL, "prod", 2, 65536).Scan(&body); err != nil || body != nil {
			t.Fatalf("oversized reference crossed wire: %v", err)
		}
		got, err := s.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{ClusterID: "prod", MaxBytes: 65536})
		if got != nil || !errors.Is(err, graph.ErrImpactSnapshotLimit) {
			t.Fatalf("oversized reference returned partial success: %v", err)
		}
	})
	t.Run("concurrent commit between resource and edge queries", func(t *testing.T) {
		reset(t)
		root := graph.Resource{Kind: "ConfigMap", Name: "settings", Namespace: "demo"}
		child, expected := storetest.ImpactReferenceFixture(t, "Pod", "v1")
		child.ClusterID = ""
		for _, resource := range []graph.Resource{root, child} {
			if err := s.UpsertResource(ctx, resource); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.UpsertEdge(ctx, graph.Edge{From: child.ID(), To: root.ID(), Type: graph.EdgeTypeUsesConfigMap}); err != nil {
			t.Fatal(err)
		}
		var mutationErr error
		observer := &impactReadObserver{afterResources: func() {
			// The public writer atomically deletes the PG/AGE resource and its
			// incident edges after the reader's first SELECT has finished.
			mutationErr = s.DeleteResource(ctx, child.ID())
		}}
		reader := newImpactTestReader(t, h.ConnStr, observer)
		before, err := reader.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{})
		if err != nil || mutationErr != nil {
			t.Fatalf("read error=%v, concurrent delete error=%v", err, mutationErr)
		}
		if len(before.Resources) != 2 || len(before.Edges) != 1 || before.Edges[0].From != child.ID() {
			t.Fatalf("mixed snapshots across concurrent commit: %+v", before)
		}
		for _, resource := range before.Resources {
			if resource.ID() == child.ID() && !reflect.DeepEqual(resource.Raw, expected.Raw) {
				t.Fatal("reference fields escaped the consistent resource read")
			}
		}
		observer.mu.Lock()
		begin, observed := observer.begin, observer.observed
		observer.mu.Unlock()
		if !observed || !strings.Contains(begin, "repeatable read") || !strings.Contains(begin, "read only") {
			t.Fatalf("did not exercise a read-only repeatable-read transaction: %q, hook=%v", begin, observed)
		}
		after, err := reader.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{})
		if err != nil || len(after.Resources) != 1 || len(after.Edges) != 0 {
			t.Fatalf("new snapshot did not observe committed delete: %+v %v", after, err)
		}
		if reader.pool.Stat().AcquiredConns() != 0 {
			t.Fatal("successful read retained a pool slot")
		}
	})
	t.Run("query cancellation releases transaction and pool slot", func(t *testing.T) {
		reset(t)
		blocker, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(ctx)
		if _, err := blocker.Exec(ctx, `LOCK TABLE public.resources IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		started := make(chan struct{}, 1)
		observer := &impactReadObserver{started: started}
		reader := newImpactTestReader(t, h.ConnStr, observer)
		readCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		finished := make(chan error, 1)
		go func() {
			g, err := reader.SnapshotImpact(readCtx, graph.ImpactSnapshotOptions{})
			if g != nil {
				err = errors.New("cancelled read returned a partial graph")
			}
			finished <- err
		}()
		select {
		case <-started:
			cancel()
		case <-time.After(3 * time.Second):
			t.Fatal("read never reached resource query")
		}
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled query error=%v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled query failed to release connection")
		}
		observer.mu.Lock()
		releases := observer.releases
		observer.mu.Unlock()
		if releases == 0 {
			t.Fatal("cancelled transaction did not release its connection")
		}
		if err := blocker.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		// pgx/puddle destroys cancelled connections asynchronously; its acquired
		// gauge can lag Release. Check actual bounded reuse of this ONE-slot pool
		// rather than requiring destruction to finish in the same instruction.
		reuseCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		if _, err := reader.SnapshotImpact(reuseCtx, graph.ImpactSnapshotOptions{}); err != nil {
			t.Fatalf("pool unusable after cancellation: %v", err)
		}
		if reader.pool.Stat().AcquiredConns() != 0 {
			t.Fatal("pool slot remained acquired after reuse")
		}
	})
	t.Run("Secret storage constraint and SQL allowlist", func(t *testing.T) {
		reset(t)
		// Current schemas must reject this synthetic legacy payload. Independently
		// exercise the projection expression against the same synthetic fixture,
		// without removing or weakening any real table constraint.
		body := `{"kind":"Secret","name":"legacy","namespace":"demo","uid":"synthetic-canary","resourceVersion":"synthetic-canary","ownerReferences":[{"kind":"Pod","name":"synthetic-canary","uid":"synthetic-canary"}],"raw":{"data":"synthetic-canary"},"annotations":{"test":"synthetic-canary"}}`
		_, err := s.pool.Exec(ctx, `INSERT INTO public.resources(id, data) VALUES ($1, $2::jsonb)`, "demo/Secret/legacy", body)
		var constraintError *pgconn.PgError
		if !errors.As(err, &constraintError) || constraintError.Code != "23514" || constraintError.ConstraintName != "resources_secret_reference_only" {
			t.Fatalf("expected Secret reference-only constraint rejection, got %v", err)
		}
		query := strings.Replace(impactResourcesSQL, "public.resources", "(SELECT $4::jsonb AS data, ''::text AS cluster_id) AS fixture", 1)
		var projected []byte
		if err := s.pool.QueryRow(ctx, query, "", 2, 4096, body).Scan(&projected); err != nil {
			t.Fatal(err)
		}
		var resource graph.Resource
		if err := json.Unmarshal(projected, &resource); err != nil || resource.Kind != "Secret" || resource.Name != "legacy" || strings.Contains(string(projected), "synthetic-canary") {
			t.Fatal("Secret fields escaped SQL projection")
		}
	})
	t.Run("oversized projected row fails before transfer", func(t *testing.T) {
		reset(t)
		body, err := json.Marshal(graph.Resource{Kind: "ConfigMap", Name: "large", Namespace: "demo", ResourceVersion: strings.Repeat("x", 8192)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO public.resources(id, data) VALUES ($1, $2::jsonb)`, "demo/ConfigMap/large", body); err != nil {
			t.Fatal(err)
		}
		// Check the SQL-side guard itself, not merely the Go-side budget.
		var transferred []byte
		if err := s.pool.QueryRow(ctx, impactResourcesSQL, "", 2, 1024).Scan(&transferred); err != nil || transferred != nil {
			t.Fatalf("oversized row crossed wire: bytes=%d error=%v", len(transferred), err)
		}
		g, err := s.SnapshotImpact(ctx, graph.ImpactSnapshotOptions{MaxBytes: 1024})
		if g != nil || !errors.Is(err, graph.ErrImpactSnapshotLimit) {
			t.Fatalf("oversized read=%+v error=%v", g, err)
		}
	})
}

type impactTraceKey struct{}

type impactReadObserver struct {
	mu             sync.Mutex
	begin          string
	observed       bool
	releases       int
	once           sync.Once
	started        chan struct{}
	afterResources func()
}

func (o *impactReadObserver) TraceRelease(_ *pgxpool.Pool, _ pgxpool.TraceReleaseData) {
	o.mu.Lock()
	o.releases++
	o.mu.Unlock()
}

func (o *impactReadObserver) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "begin") {
		o.mu.Lock()
		o.begin = data.SQL
		o.mu.Unlock()
	}
	if data.SQL == impactResourcesSQL && o.started != nil {
		select {
		case o.started <- struct{}{}:
		default:
		}
	}
	return context.WithValue(ctx, impactTraceKey{}, data.SQL)
}

func (o *impactReadObserver) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	query, _ := ctx.Value(impactTraceKey{}).(string)
	if query == impactResourcesSQL && data.Err == nil && o.afterResources != nil {
		o.once.Do(func() {
			o.mu.Lock()
			o.observed = true
			o.mu.Unlock()
			o.afterResources()
		})
	}
}

func newImpactTestReader(t *testing.T, dsn string, tracer pgx.QueryTracer) *Store {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &Store{pool: pool}
}

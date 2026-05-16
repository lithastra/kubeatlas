-- KubeAtlas Tier 2 schema, version 6.
--
-- P3-T8 (F-113) — full-text search. Adds a generated tsvector column
-- over each resource's searchable text and a GIN index on it, so
-- /api/v1/search runs as one indexed match in the database instead
-- of the Phase 1 path (store.Snapshot + a Go-side linear scan, the
-- same OOM pattern P3-T0a removed from the cluster/namespace views).
--
-- Indexed surface and weights:
--   A  name        — the field users search by most; ranked highest.
--   B  kind        — "find the Ingresses called foo".
--   B  namespace
--   C  label values
--
-- Deliberately NOT indexed (see ADR 0011):
--   * Secret data — sensitive, and base64 text is not searchable.
--   * annotations / spec — the last-applied-configuration annotation
--     alone would bloat every row's tsvector. Widening the surface
--     is a v1.2 decision.
--
-- The `resources.data` JSONB is the marshalled graph.Resource:
-- name / kind / namespace are top-level string fields and labels is
-- a top-level string->string object (matching 001_initial.sql's
-- idx_resources_* expression indexes). Every function used here is
-- IMMUTABLE, which a GENERATED ... STORED column requires:
-- to_tsvector with an explicit 'simple' config, setweight, tsvector
-- concatenation, coalesce, the -> / ->> operators, and the two-arg
-- jsonb_path_query_array.
--
-- All statements idempotent. No AGE involvement — search is plain
-- SQL. Every object is fully-qualified with `public.`: an
-- AGE-enabled database carries `ag_catalog` at the front of
-- search_path, so an unqualified ALTER TABLE / CREATE INDEX could
-- resolve against the wrong schema (see 005_snapshots.sql).

ALTER TABLE public.resources
    ADD COLUMN IF NOT EXISTS search_tsv tsvector
    GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', coalesce(data->>'name', '')), 'A') ||
        setweight(to_tsvector('simple', coalesce(data->>'kind', '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(data->>'namespace', '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(jsonb_path_query_array(data, '$.labels.*')::text, '')), 'C')
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_resources_search
    ON public.resources USING gin (search_tsv);

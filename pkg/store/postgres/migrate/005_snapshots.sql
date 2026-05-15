-- KubeAtlas Tier 2 schema, version 5.
--
-- P3-T2 (F-111 part 1) — historical snapshots. Two tables:
--
--   resource_events  — append-only stream of every add/update/delete
--                      the informer observes. INSERT-only: never
--                      UPDATEd, never DELETEd on the write path
--                      (retention prune in P3-T4 is the only DELETE,
--                      and it deletes whole time ranges, never edits
--                      a row). A "correction" is a compensating
--                      event, not a mutation.
--   snapshot_meta    — one row per periodic full-sync marker, so the
--                      diff endpoint (P3-T5) can anchor "5 min ago"
--                      to a known-complete point rather than to an
--                      arbitrary mid-stream event.
--
-- Tier 2 only — invariant 2.2. The memory store keeps at most a
-- small bounded ring buffer for test support; the /api/v1/snapshots
-- endpoints return 503 on Tier 1 (enforced in the API layer, P3-T5).
--
-- cluster_id is carried now (DEFAULT '') so F-201 multi-cluster
-- federation does not need a schema change later — it is an
-- append-only column addition consistent with invariant 2.4.
--
-- All statements idempotent. No AGE involvement — the event stream
-- is plain SQL; AGE is a query-time concern only (anti-pattern: do
-- not write events through Cypher).
--
-- Every object is fully-qualified with `public.`. AGE-enabled
-- databases carry `ag_catalog` at the front of search_path, so an
-- unqualified CREATE TABLE would land in ag_catalog instead of
-- public. 001_initial.sql sidesteps this by creating its tables
-- before its SET search_path; qualifying explicitly is the
-- search_path-immune equivalent (same trick applyMigration uses for
-- its schema_migrations bookkeeping INSERT).

CREATE TABLE IF NOT EXISTS public.resource_events (
    id               BIGSERIAL PRIMARY KEY,
    ts               TIMESTAMPTZ NOT NULL DEFAULT now(),
    cluster_id       TEXT NOT NULL DEFAULT '',
    namespace        TEXT NOT NULL DEFAULT '',
    kind             TEXT NOT NULL,
    uid              TEXT NOT NULL DEFAULT '',
    name             TEXT NOT NULL,
    event_type       TEXT NOT NULL CHECK (event_type IN ('add', 'update', 'delete')),
    resource_version TEXT NOT NULL DEFAULT '',
    data             JSONB
);

-- idx_events_ts powers the time-window scan the diff endpoint runs
-- ("everything between :from and :to"). Most queries also pin a
-- namespace, so idx_events_ns_ts is the composite that lets PG
-- satisfy "namespace = X AND ts BETWEEN ..." from one index.
CREATE INDEX IF NOT EXISTS idx_events_ts
    ON public.resource_events (ts);
CREATE INDEX IF NOT EXISTS idx_events_ns_ts
    ON public.resource_events (namespace, ts);
-- idx_events_uid_ts: per-resource history ("show me every change to
-- this Deployment"), uid is the stable identity across renames.
CREATE INDEX IF NOT EXISTS idx_events_uid_ts
    ON public.resource_events (uid, ts);

CREATE TABLE IF NOT EXISTS public.snapshot_meta (
    id              BIGSERIAL PRIMARY KEY,
    ts              TIMESTAMPTZ NOT NULL DEFAULT now(),
    cluster_id      TEXT NOT NULL DEFAULT '',
    resource_count  BIGINT NOT NULL DEFAULT 0,
    edge_count      BIGINT NOT NULL DEFAULT 0,
    duration_ms     BIGINT NOT NULL DEFAULT 0,
    trigger         TEXT NOT NULL CHECK (trigger IN ('periodic', 'manual', 'startup'))
);

CREATE INDEX IF NOT EXISTS idx_snapshot_meta_ts
    ON public.snapshot_meta (ts);

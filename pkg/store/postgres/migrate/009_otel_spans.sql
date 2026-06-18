-- KubeAtlas Tier 2 schema, version 9.
--
-- F-204 — OpenTelemetry trace overlay, storage half. One table:
--
--   otel_spans  — trace spans received over OTLP/gRPC. Written by the
--                 receiver's batch workers, queried by the overlay API
--                 (a later task), and pruned by the hourly retention
--                 worker. span_id is the natural key, so a re-sent span
--                 (OTLP retransmit) upserts rather than duplicating.
--
-- Tier 2 only — invariant 2.2 / 2.5. Spans are never stored on Tier 1
-- (the memory backend has no span store); the /api/v1/otel/* endpoints
-- return 503 on Tier 1 (enforced in the API layer, later task). Span
-- ingestion is opt-in (otel.enabled, default false): on a default
-- install this table is created but never written.
--
-- The K8s columns (k8s_namespace / k8s_pod / k8s_deployment) are
-- lifted from each span's resource attributes at write time so the
-- correlator can join to the resources table with an indexed column
-- lookup rather than a JSONB probe.
--
-- All statements idempotent. No AGE involvement — spans are a plain
-- relational concern; AGE is a query-time graph concern only (same
-- anti-pattern guard 005_snapshots.sql states: do not write spans
-- through Cypher, do not touch any vertex/edge label).
--
-- Every object is fully-qualified with `public.`. AGE-enabled
-- databases carry `ag_catalog` at the front of search_path, so an
-- unqualified CREATE TABLE would land in ag_catalog instead of
-- public — qualifying explicitly is the search_path-immune form
-- (same trick 005_snapshots.sql and the migration bookkeeping use).

CREATE TABLE IF NOT EXISTS public.otel_spans (
    trace_id       TEXT NOT NULL,
    span_id        TEXT NOT NULL PRIMARY KEY,
    parent_span_id TEXT NOT NULL DEFAULT '',
    service_name   TEXT NOT NULL DEFAULT '',
    k8s_namespace  TEXT NOT NULL DEFAULT '',
    k8s_pod        TEXT NOT NULL DEFAULT '',
    k8s_deployment TEXT NOT NULL DEFAULT '',
    start_time     TIMESTAMPTZ NOT NULL,
    duration_ns    BIGINT NOT NULL DEFAULT 0,
    attributes     JSONB,
    received_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- idx_otel_spans_service_time powers the overlay query ("recent spans
-- for service X"): service_name equality + start_time window, served
-- from one index, newest-first.
CREATE INDEX IF NOT EXISTS idx_otel_spans_service_time
    ON public.otel_spans (service_name, start_time DESC);
-- idx_otel_spans_received_at makes the retention sweep's
-- `received_at < cutoff` an index range scan (mirrors idx_events_ts).
CREATE INDEX IF NOT EXISTS idx_otel_spans_received_at
    ON public.otel_spans (received_at);
-- idx_otel_spans_trace: per-trace assembly for the correlator
-- (walk a trace's spans to infer runtime call edges).
CREATE INDEX IF NOT EXISTS idx_otel_spans_trace
    ON public.otel_spans (trace_id);

-- 010_otel_runtime_edges.sql
--
-- F-204 part 2 (P5-T5): observed runtime call edges, inferred by the
-- OTel correlator from otel_spans (a parent span in service A calling a
-- child span in service B becomes a caller -> callee edge).
--
-- Kept in its own plain-SQL table, off the AGE graph and off
-- resource_events: runtime edges are an opt-in overlay served ONLY by
-- GET /api/v1/otel/overlay and must never appear in /api/v1/graph or
-- /api/v1alpha1/graph, which invariant 2.2 keeps byte-identical to
-- v1.4. No AGE / Cypher, so this never runs inside withAGETx.
--
-- (from_id, to_id) is the primary key: the correlator upserts, folding
-- repeated observations of the same call into one row (LEAST/GREATEST
-- on the timestamps, GREATEST on call_count). The row set is bounded by
-- the number of distinct resource pairs, so the retention DELETE is
-- unbounded (unlike the batched otel_spans sweep).
CREATE TABLE otel_runtime_edges (
    from_id      TEXT NOT NULL,
    to_id        TEXT NOT NULL,
    from_service TEXT NOT NULL DEFAULT '',
    to_service   TEXT NOT NULL DEFAULT '',
    namespace    TEXT NOT NULL DEFAULT '',
    first_seen   TIMESTAMPTZ NOT NULL,
    last_seen    TIMESTAMPTZ NOT NULL,
    call_count   BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY (from_id, to_id)
);

-- Overlay queries filter by namespace and a last_seen recency floor.
CREATE INDEX idx_otel_runtime_edges_ns ON otel_runtime_edges (namespace, last_seen DESC);
-- Retention prunes on last_seen.
CREATE INDEX idx_otel_runtime_edges_last_seen ON otel_runtime_edges (last_seen);

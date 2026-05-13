-- Composite indexes that let cluster + namespace aggregation
-- (KindCountsByNamespace, NamespaceSubgraph) execute as index-only
-- scans instead of materialising every row through the planner.
--
-- Before P3-T0a:
--   * cluster_view / namespace_view called store.Snapshot which
--     SELECTed every (id, data) row and unmarshalled each JSONB blob
--     into a graph.Resource. On a 6-7K resource cluster this
--     allocated 50-200 MB per request and OOM-killed the API pod.
-- After P3-T0a:
--   * KindCountsByNamespace executes
--       SELECT data->>'namespace', data->>'kind', COUNT(*) FROM resources
--       GROUP BY data->>'namespace', data->>'kind';
--     The composite expression index lets the planner satisfy this
--     entirely from the index without touching the heap.
--   * CrossNamespaceEdgeCounts joins edges to resources twice and
--     groups by namespace pairs; the existing idx_resources_namespace
--     (from 001_initial.sql) is enough on the join side.
--   * NamespaceSubgraph filters resources by data->>'namespace' and
--     uses the existing idx_resources_namespace; no new index needed
--     for the resource fetch. The edges join uses idx_edges_from /
--     idx_edges_to from 001_initial.sql.
--
-- Idempotent — IF NOT EXISTS guards every CREATE.

CREATE INDEX IF NOT EXISTS idx_resources_namespace_kind
    ON resources ((data->>'namespace'), (data->>'kind'));

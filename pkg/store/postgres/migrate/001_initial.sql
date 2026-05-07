-- KubeAtlas Tier 2 schema, version 1.
--
-- This migration owns:
--   * Plain Postgres tables (resources, edges) — moved from the
--     P2-T2 inline DDL.
--   * Apache AGE bootstrap: extension, graph, vertex labels, and
--     edge labels for every Phase 0/1 GVR plus the eight core edge
--     types in graph.AllEdgeTypes.
--
-- All statements are idempotent. Running this migration twice on
-- the same database is a no-op.

-- ---------------------------------------------------------------
-- Plain SQL tables (P2-T2 baseline; AGE traversal layered on in P2-T4).
-- ---------------------------------------------------------------

CREATE TABLE IF NOT EXISTS resources (
    id   TEXT PRIMARY KEY,
    data JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_resources_kind
    ON resources ((data->>'kind'));
CREATE INDEX IF NOT EXISTS idx_resources_namespace
    ON resources ((data->>'namespace'));
CREATE INDEX IF NOT EXISTS idx_resources_labels
    ON resources USING gin ((data->'labels'));

CREATE TABLE IF NOT EXISTS edges (
    from_id TEXT NOT NULL,
    to_id   TEXT NOT NULL,
    type    TEXT NOT NULL,
    PRIMARY KEY (from_id, to_id, type)
);
CREATE INDEX IF NOT EXISTS idx_edges_to   ON edges (to_id);
CREATE INDEX IF NOT EXISTS idx_edges_from ON edges (from_id);

-- ---------------------------------------------------------------
-- Apache AGE bootstrap.
-- LOAD 'age' is session-scoped; the schema runner re-issues it on
-- every connection it owns. SET search_path is also session-scoped
-- but harmless to repeat.
-- ---------------------------------------------------------------

CREATE EXTENSION IF NOT EXISTS age;
LOAD 'age';
-- SET LOCAL keeps search_path scoped to this transaction. Without
-- LOCAL the change persists on the pooled connection and silently
-- routes later queries (e.g. SELECT MAX(version) FROM
-- schema_migrations) to ag_catalog instead of public.
SET LOCAL search_path = ag_catalog, "$user", public;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM ag_catalog.ag_graph WHERE name = 'kubeatlas'
    ) THEN
        PERFORM ag_catalog.create_graph('kubeatlas'::name);
    END IF;
END $$;

-- ---------------------------------------------------------------
-- Vertex labels: one per K8s Kind covered by Phase 0/1 informers.
-- CRDs land at runtime through the dynamic discovery pipeline
-- (P2-T10) and create their labels lazily via create_vlabel.
-- ---------------------------------------------------------------

DO $$
DECLARE
    kinds text[] := ARRAY[
        'Namespace', 'Node',
        'Pod', 'Service', 'ConfigMap', 'Secret',
        'PersistentVolume', 'PersistentVolumeClaim', 'ServiceAccount',
        'Deployment', 'ReplicaSet', 'StatefulSet', 'DaemonSet',
        'Job', 'CronJob',
        'Ingress', 'Gateway', 'HTTPRoute'
    ];
    k text;
BEGIN
    FOREACH k IN ARRAY kinds LOOP
        IF NOT EXISTS (
            SELECT 1 FROM ag_catalog.ag_label l
            JOIN ag_catalog.ag_graph g ON l.graph = g.graphid
            WHERE g.name = 'kubeatlas' AND l.name = k AND l.kind = 'v'
        ) THEN
            PERFORM ag_catalog.create_vlabel('kubeatlas'::cstring, k::cstring);
        END IF;
    END LOOP;
END $$;

-- ---------------------------------------------------------------
-- Edge labels: the eight core types from graph.AllEdgeTypes.
-- Phase 2 RBAC edges (BINDS_SUBJECT, BINDS_ROLE) land in P2-T14's
-- migration; Rego-derived edges (e.g. STORES_IN, ROUTES_TO from
-- cert-manager) are created at runtime when first observed.
-- ---------------------------------------------------------------

DO $$
DECLARE
    types text[] := ARRAY[
        'OWNS',
        'USES_CONFIGMAP',
        'USES_SECRET',
        'MOUNTS_VOLUME',
        'SELECTS',
        'USES_SERVICEACCOUNT',
        'ROUTES_TO',
        'ATTACHED_TO'
    ];
    t text;
BEGIN
    FOREACH t IN ARRAY types LOOP
        IF NOT EXISTS (
            SELECT 1 FROM ag_catalog.ag_label l
            JOIN ag_catalog.ag_graph g ON l.graph = g.graphid
            WHERE g.name = 'kubeatlas' AND l.name = t AND l.kind = 'e'
        ) THEN
            PERFORM ag_catalog.create_elabel('kubeatlas'::cstring, t::cstring);
        END IF;
    END LOOP;
END $$;

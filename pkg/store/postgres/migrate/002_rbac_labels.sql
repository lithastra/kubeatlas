-- KubeAtlas Tier 2 schema, version 2.
--
-- Adds AGE vertex labels for the four RBAC resources P2-T14
-- introduced into CoreGVRs (RoleBinding / ClusterRoleBinding /
-- Role / ClusterRole) and edge labels for the two new edge types
-- the RBAC extractors emit (BINDS_SUBJECT / BINDS_ROLE).
--
-- Without this migration the AGE vertex/edge label allowlist in
-- pkg/extractor/rego/cypher.go silently drops RBAC resources from
-- the AGE side of double-write. PG persistence already covers
-- them — the issue is only Cypher traversal (BlastRadius / future
-- impact analysis) missing the RBAC chain.
--
-- All statements are idempotent. Same DO-block / IF NOT EXISTS
-- pattern as 001_initial.sql.

LOAD 'age';
SET LOCAL search_path = ag_catalog, "$user", public;

DO $$
DECLARE
    kinds text[] := ARRAY[
        'RoleBinding',
        'ClusterRoleBinding',
        'Role',
        'ClusterRole'
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

DO $$
DECLARE
    types text[] := ARRAY[
        'BINDS_SUBJECT',
        'BINDS_ROLE'
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

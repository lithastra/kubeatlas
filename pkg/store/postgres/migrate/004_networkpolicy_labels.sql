-- KubeAtlas Tier 2 schema, version 4.
--
-- P3-T1 (F-109) — adds the AGE vertex label for NetworkPolicy
-- and the three edge labels the NetworkPolicy extractors emit
-- (SELECTS_NP / ALLOWS_FROM / ALLOWS_TO).
--
-- Without this migration the AGE vertex/edge label allowlist
-- silently drops NetworkPolicy nodes and their declared edges
-- from the AGE side of double-write. PG persistence already
-- covers them via runtime label creation (see 001_initial.sql
-- comment) but pre-registering keeps Cypher traversals
-- (BlastRadius / future impact analysis) consistent the first
-- time a NetworkPolicy is observed.
--
-- All statements are idempotent. Same DO-block / IF NOT EXISTS
-- pattern as 001_initial.sql and 002_rbac_labels.sql.
--
-- Note: no `LOAD 'age'` here for the same reason as 002 — CNPG's
-- restricted_load_libraries blocks LOAD for the non-superuser
-- app role; Postgres autoloads the AGE shared object the first
-- time a function under ag_catalog is called.

SET LOCAL search_path = ag_catalog, "$user", public;

DO $$
DECLARE
    kinds text[] := ARRAY[
        'NetworkPolicy'
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
        'SELECTS_NP',
        'ALLOWS_FROM',
        'ALLOWS_TO'
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

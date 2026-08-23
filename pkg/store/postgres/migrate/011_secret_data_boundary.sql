-- KubeAtlas Tier 2 schema, version 11.
--
-- Security boundary:
--   * Kubernetes Secret resources are reduced to synthetic reference-only
--     nodes. Kubernetes RBAC cannot return Secret metadata without returning
--     the complete object, so v1.5.2 no longer watches Secrets.
--   * Historical resource_events are metadata-only for every resource kind.
--
-- This migration intentionally destroys previously retained payload data. It
-- runs transactionally before the API and informer become ready.

UPDATE public.resources
SET data = jsonb_strip_nulls(jsonb_build_object(
    'kind', 'Secret',
    'name', data -> 'name',
    'namespace', data -> 'namespace',
    'annotations', jsonb_build_object('kubeatlas.io/reference-only', 'true'),
    'clusterId', data -> 'clusterId'
))
WHERE data ->> 'kind' = 'Secret';

UPDATE public.resource_events
SET data = NULL
WHERE data IS NOT NULL;

-- Secret metadata previously produced outgoing OWNS edges, and old incoming
-- references may be stale. Remove every incident edge from both storage
-- representations. The initial informer sync recreates current incoming
-- reference edges before readiness; reference-only Secret nodes never create
-- outgoing edges.
DELETE FROM public.edges
WHERE from_id IN (SELECT id FROM public.resources WHERE data ->> 'kind' = 'Secret')
   OR to_id IN (SELECT id FROM public.resources WHERE data ->> 'kind' = 'Secret');

SET LOCAL search_path = ag_catalog, "$user", public;
SELECT * FROM ag_catalog.cypher('kubeatlas'::name, $$
    MATCH (s:Secret) DETACH DELETE s
$$::cstring) AS (v agtype);

ALTER TABLE public.resource_events
    DROP CONSTRAINT IF EXISTS resource_events_metadata_only;
ALTER TABLE public.resource_events
    ADD CONSTRAINT resource_events_metadata_only CHECK (data IS NULL);

ALTER TABLE public.resources
    DROP CONSTRAINT IF EXISTS resources_secret_reference_only;
ALTER TABLE public.resources
    ADD CONSTRAINT resources_secret_reference_only CHECK (
        -- Treat either the structured kind or the canonical resource ID as a
        -- Secret signal. PostgreSQL CHECK constraints accept NULL results, so
        -- relying only on data->>'kind' would let a malformed direct write
        -- omit kind and bypass the reference-only branch.
        (
            data ->> 'kind' IS DISTINCT FROM 'Secret'
            AND id !~ '(^|:)[^/]+/Secret/[^/]+$'
        )
        OR (
            -- Fail closed: a Secret row may contain only the identity fields
            -- emitted by graph.SecretReferenceResource. A denylist would let
            -- a new or misspelled payload field bypass the boundary.
            jsonb_typeof(data) = 'object'
            AND (data - ARRAY['kind', 'name', 'namespace', 'annotations', 'clusterId']::text[]) = '{}'::jsonb
            AND jsonb_typeof(data -> 'kind') = 'string'
            AND data ->> 'kind' = 'Secret'
            AND jsonb_typeof(data -> 'name') = 'string'
            AND data ->> 'name' <> ''
            AND jsonb_typeof(data -> 'namespace') = 'string'
            AND data ->> 'namespace' <> ''
            AND data -> 'annotations' = jsonb_build_object('kubeatlas.io/reference-only', 'true')
            AND (
                NOT (data ? 'clusterId')
                OR jsonb_typeof(data -> 'clusterId') = 'string'
            )
            AND id = (
                CASE
                    WHEN COALESCE(data ->> 'clusterId', '') = '' THEN ''
                    ELSE (data ->> 'clusterId') || ':'
                END
                || (data ->> 'namespace') || '/Secret/' || (data ->> 'name')
            )
        )
    );

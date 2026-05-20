-- P3-T20 federation foundation: track each resource's ClusterID at
-- the column level so multi-cluster queries (ListResourcesInCluster,
-- GetEdgesAcrossClusters) can be indexed instead of scanning every
-- JSONB blob.
--
-- The column is GENERATED from the existing JSONB `data->>'clusterId'`,
-- so UpsertResource keeps writing a single column (data) and the
-- cluster tag follows for free. Empty string (the default) is the
-- single-cluster path — every pre-P3-T20 row reads back as
-- cluster_id='', matching the v1.2 / single-cluster behaviour.
ALTER TABLE resources
    ADD COLUMN IF NOT EXISTS cluster_id TEXT
    GENERATED ALWAYS AS (COALESCE(data->>'clusterId', '')) STORED;

CREATE INDEX IF NOT EXISTS idx_resources_cluster_id
    ON resources (cluster_id);

-- 008_edge_attributes.sql
--
-- Add an optional attributes bag to edges. ENFORCES edges
-- (Gatekeeper/Kyverno policy integration) carry violation status here
-- ("violated", "violation_message"); every other edge type leaves it
-- as the empty object. JSONB keeps the column queryable without a
-- schema change per attribute.
--
-- Additive and backfilled with '{}', so existing rows and readers are
-- unaffected.

ALTER TABLE edges
    ADD COLUMN IF NOT EXISTS attributes JSONB NOT NULL DEFAULT '{}'::jsonb;

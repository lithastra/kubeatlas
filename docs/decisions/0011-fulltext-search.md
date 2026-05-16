# 0011 — Full-text search backend

- Status: accepted
- Date: 2026-05-16
- Task: P3-T8 (F-113)

## Context

`/api/v1/search` shipped in Phase 1 as a linear, case-insensitive
substring scan. Its implementation calls `store.Snapshot()` —
materialising every resource (including the full K8s payload) into
the API process — and then scans in Go. That is the exact pattern
P3-T0a removed from the cluster/namespace views because it OOM-kills
the API pod on clusters past ~5–7K resources. Search must be pushed
down into the store, and the v1.1 target is **P95 < 200 ms on a
10K-resource cluster**.

Three backends were considered.

### A. PostgreSQL `tsvector` + GIN index (Tier 2)

A generated `tsvector` column on `resources`, indexed with GIN. The
query becomes a single indexed `@@` match with `ts_rank` ordering.

- **+** No new dependency, no new process. The GIN index handles
  10K–100K resources comfortably.
- **+** Search inherits the Tier 2 story KubeAtlas already tells for
  historical snapshots (F-111): "the richer features want
  PostgreSQL". No new operational concept for users to learn.
- **+** Ranking, weighting, and stop-word handling come for free.
- **−** Indexed search is Tier 2 only. Tier 1 must fall back.

### B. `bleve` (pure-Go full-text index)

An in-process inverted index, usable on both tiers.

- **+** Indexed search on Tier 1 too.
- **−** A second index to keep coherent with the graph, with its own
  memory budget — re-introducing the per-process memory pressure
  P3-T0a just removed.
- **−** A non-trivial new dependency for a feature PostgreSQL
  already does well.

### C. External Elasticsearch

- **−** A whole external system to operate for one endpoint.
  Disproportionate. Rejected outright.

## Decision

**Adopt A — PostgreSQL `tsvector` + GIN.**

`Search` becomes a `GraphStore` method:

- **Tier 2 (PostgreSQL)** — indexed `tsvector` match, `ts_rank`
  ordering, executed entirely in the database. This is the path the
  200 ms target is measured against.
- **Tier 1 (memory)** — a naive linear scan over the in-memory
  resource map. It is correct but unranked-by-index and O(N); the
  API response carries an explicit warning so an operator on a
  large Tier 1 cluster understands why search is slow and that
  Tier 2 fixes it. Search must not silently "seem to work".

Tier 1 indexed search (option B) is deferred. If users report they
need fast search without PostgreSQL, a `bleve` fallback can be added
in v1.2 behind the same `GraphStore.Search` method — the interface
chosen here does not preclude it.

## Consequences

- New migration `006_search_index.sql`: a `STORED` generated
  `tsvector` column over name / kind / namespace / label values,
  plus a GIN index.
- `GraphStore` gains `Search`. The query model is intentionally
  small for v1.1 — free-text terms plus `kind:` / `namespace:`
  field filters. A richer query DSL is explicitly out of scope
  until there is user feedback (v1.2+).
- `Secret` `data` is **not** indexed — it is sensitive, and
  base64-encoded values are not meaningfully searchable anyway.
  Annotations and `spec` are also out of the index for v1.1: the
  `last-applied-configuration` annotation alone would bloat every
  row's `tsvector`. Widening the indexed surface is a v1.2 decision.

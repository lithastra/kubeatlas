---
sidebar_position: 5
title: API versioning
---

# API versioning

The KubeAtlas HTTP API ships under two prefixes that are served
by the same handlers:

| Prefix | Status | Notes |
|---|---|---|
| `/api/v1alpha1/*` | Frozen surface | Phase 1 shape; no field will be removed or renamed. |
| `/api/v1/*` | GA | Phase 2 superset; adds graph-analysis enrichment fields on `ResourceDetailResponse`. |

Both prefixes hit the same store query path. The only difference
is serialisation — `/api/v1/...` returns a `ResourceDetailResponseV1`
that carries `blastRadiusCount`, `isOrphan`, and `inCycle` fields
on top of the v1alpha1 shape; `/api/v1alpha1/...` returns the
original three-field bundle.

## What "frozen" means

The CI job `api-compat-check` compares the build's emitted v1alpha1
spec against `api/openapi-v1alpha1.json` (the v0.1.0 baseline) on
every PR. The job fails when any of the following changes:

- A path is removed.
- A method is removed from an existing path.
- A schema is removed.
- A property is removed from an existing schema.
- A property's type changes.
- A previously-required field becomes optional.
- A previously-optional field becomes required.

Additions are always fine: a new endpoint, a new optional field, a
new schema. The intent is that v0.1.0-era curl + jq scripts never
break across the v0.x → v1.x → v2.x trajectory of the project.

## Adding a new endpoint

The route table in `pkg/api/routes.go` is the single source of
truth. Add the entry under v1alpha1 form (the canonical pattern is
`/api/v1alpha1/<path>`); the server registers each route at the
matching `/api/v1/<path>` automatically. The same handler powers
both versions.

After adding the endpoint, regenerate the baseline so the new
schema lands in `api/openapi-v1alpha1.json`:

```bash
go run ./tools/api-compat-check/dump > api/openapi-v1alpha1.json
```

Commit the regenerated file alongside the route addition.

## Adding a v1-only field

For fields the v1alpha1 surface should not see (typically
graph-analysis enrichments that pre-Phase-2 clients didn't ask
for), declare them on a sibling DTO type — see
`ResourceDetailResponseV1` in `pkg/api/handlers.go`. The handler
branches on `apiVersionFor(r)` and serializes the version-
appropriate type. The OpenAPI spec for v1 picks up the sibling
schema automatically through `openAPIComponentsFor`.

Do NOT add v1-only fields to existing v1alpha1 DTOs. The shared
JSON shape between the two versions is part of the
"v1alpha1 → v1 is a no-touch upgrade" promise.

## Deprecation timeline

The eventual retirement story for v1alpha1:

| Release | v1alpha1 status |
|---|---|
| v1.0.x | Fully supported, served alongside v1. |
| v1.1.x | `Deprecation: true` response header on every v1alpha1 response (no functional change). |
| v2.0.x | Removed. The migration window from `Deprecation: true` to removal is at least one minor release line. |

Practical consequences:

- Scripts and dashboards that pin `/api/v1alpha1/...` keep working
  through every v1.x release.
- A user upgrading directly from v0.1.0 to v2.0 has at least one
  minor-line warning window; check the `Deprecation` header in
  CI logs and migrate.
- If your tool can drift to `/api/v1/...` early, do — the
  enrichment fields are useful and the schema is a strict
  superset.

## Tracking v1alpha1 usage

From v1.4 the server counts every request by API version and endpoint
and exposes the split on `/metrics`:

```
kubeatlas_api_v1alpha1_requests_total{endpoint="graph"}
kubeatlas_api_v1_requests_total{endpoint="graph"}
```

The only label is the endpoint — the matched route, never request
values or caller identity — so cardinality is bounded by the route
table. The ratio

```
v1alpha1 / (v1alpha1 + v1)
```

is the data behind the v1alpha1 retirement decision: when it stays below
5% over a 30-day window (or the six-month announcement period from v1.4
elapses), v1alpha1 becomes safe to remove in v2.0. The series are
emitted even at zero traffic, so a dashboard can chart them from the day
v1.4 ships. The counts stay inside the cluster — they are never sent
through the opt-in telemetry pipeline.

## Why two prefixes instead of one with a content-type negotiation

Path-based versioning beats `Accept` headers in practice for
operator tooling: every shell pipeline (`curl`, `jq`, `kubectl
exec`, `wget`) understands paths; not all of them honour
content-type negotiation cleanly. The path also makes it trivial
to grep for "what version is this script using" — search for the
prefix.

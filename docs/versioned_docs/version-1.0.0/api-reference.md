---
sidebar_position: 4
title: API reference
---

# API reference

KubeAtlas serves a small REST API on port 8080 (proxied through the
`kubeatlas` Service when installed via Helm). The same Go binary
serves both the CLI and the API.

Two API surfaces are served from the same handlers:

- **`/api/v1/*`** — the v1.0 GA surface. Adds graph-analysis
  enrichment fields (`blastRadiusCount`, `isOrphan`, `inCycle`)
  on the resource-detail bundle.
- **`/api/v1alpha1/*`** — the frozen v0.1.0 surface. Every field
  is guaranteed to stay; CI's `api-compat-check` rejects any
  PR that would remove or rename one.

See [API versioning](./concepts/api-versioning.md) for the full
coexistence + retirement story.

The **canonical** description is the OpenAPI 3.0 document the server
emits at `/api/v1/openapi.json` (or `/api/v1alpha1/openapi.json`
for the frozen surface). This page is a hand-written companion
that's easier to skim — if it ever disagrees with the OpenAPI
document, trust the OpenAPI document.

## Conventions

- All paths return `Content-Type: application/json` unless noted.
- Cluster-scoped resources use `_` as the namespace placeholder in
  path templates (e.g. `/api/v1/resources/_/Namespace/default`).
- 4xx errors return `{ "error": "<message>" }`; 5xx errors return the
  same shape, with the message suitable for surfacing to operators.
- The API does not paginate. Every list endpoint returns the full
  result; the search endpoint caps at `limit=200`.
- The examples below use `/api/v1/` URLs. Swap the prefix to
  `/api/v1alpha1/` for the frozen surface (response shapes are
  identical except for the resource-detail enrichment fields).

## Health and observability

### `GET /healthz`

Liveness probe. Returns `200 OK` while the process can serve HTTP.
Never gates on cluster state, so a pod stuck without an apiserver
still passes liveness.

### `GET /readyz`

Readiness probe. Returns `200` once the informer cache has completed
initial sync; `503` until then. Use this — not `/healthz` — for
"is KubeAtlas actually serving the graph yet?"

### `GET /metrics`

Prometheus text exposition. Hand-rolled (no client_golang
dependency); covers goroutine count, informer sync state, and
request counts by method and status. `Content-Type: text/plain`.

## Graph queries

### `GET /api/v1/graph`

Returns an aggregated `View` at one of four levels.

| Query param | Required | Meaning |
|---|---|---|
| `level` | yes | One of `cluster`, `namespace`, `workload`, `resource` |
| `namespace` | for `namespace`/`workload`/`resource` | Target namespace |
| `kind` | for `workload`/`resource` | Resource Kind (e.g. `Deployment`) |
| `name` | for `workload`/`resource` | Resource name |

Examples:

```bash
# Cluster summary — one node per namespace.
curl -s 'http://localhost:8080/api/v1/graph?level=cluster'

# Namespace view — one node per workload.
curl -s 'http://localhost:8080/api/v1/graph?level=namespace&namespace=petclinic'

# Workload view — the workload plus its referenced resources (BFS via OWNS).
curl -s 'http://localhost:8080/api/v1/graph?level=workload&namespace=petclinic&kind=Deployment&name=customers'

# Single-resource view — resource + one-hop neighbours, capped at 30.
curl -s 'http://localhost:8080/api/v1/graph?level=resource&namespace=petclinic&kind=Pod&name=customers-xxx'
```

Response shape (`View`):

```json
{
  "level": "namespace",
  "scope": { "namespace": "petclinic" },
  "nodes": [ { "id": "...", "kind": "...", "namespace": "...", "name": "...", "label": "..." } ],
  "edges": [ { "from": "...", "to": "...", "type": "OWNS" } ],
  "truncated": false,
  "mermaid": "flowchart LR ..."   // only populated for level=resource
}
```

The ten built-in edge types are: `OWNS`, `USES_CONFIGMAP`,
`USES_SECRET`, `MOUNTS_VOLUME`, `SELECTS`, `USES_SERVICEACCOUNT`,
`ROUTES_TO`, `ATTACHED_TO`, `BINDS_SUBJECT`, `BINDS_ROLE`.
Additional types come from any loaded
[Rego rule packs](./concepts/rego-rules.md).

## Resource detail

### `GET /api/v1/resources/{namespace}/{kind}/{name}`

Returns the resource plus its incoming and outgoing edges in one
round-trip, with three v1-only enrichment fields. Use `_` for
cluster-scoped resources.

```bash
curl -s 'http://localhost:8080/api/v1/resources/petclinic/Deployment/customers' | jq
```

Response shape (`ResourceDetailResponseV1`):

```json
{
  "resource":         { "kind": "Deployment", "namespace": "petclinic", "name": "customers", "labels": {...}, "annotations": {...}, "raw": {...} },
  "incoming":         [ { "from": "...", "to": "...", "type": "OWNS" } ],
  "outgoing":         [ { "from": "...", "to": "...", "type": "USES_CONFIGMAP" } ],
  "blastRadiusCount": 7,
  "isOrphan":         false,
  "inCycle":          false
}
```

The `/api/v1alpha1/resources/...` twin returns the same body
without the bottom three fields — that's the frozen v0.1.0
shape, deliberately preserved.

### `GET /api/v1/resources/{namespace}/{kind}/{name}/incoming`

Just the incoming edges — `from` is the other end, `to` is this
resource. Same path conventions as above.

### `GET /api/v1/resources/{namespace}/{kind}/{name}/outgoing`

Just the outgoing edges.

## Graph analysis

### `GET /api/v1/blast-radius/{namespace}/{kind}/{name}`

Walks incoming edges from the target and returns every resource
that depends on it, transitively. See
[Blast radius](./concepts/blast-radius.md) for the conceptual
model.

| Query param | Default | Meaning |
|---|---|---|
| `max_depth` | `5` | Path-length cap (hard ceiling 10) |
| `edge_types` | `""` | Comma-separated allowlist of edge labels |
| `include_source` | `false` | Include the start resource in the result |

### `GET /api/v1/orphans`

Returns every resource that is either a non-top-level kind with
zero incoming edges or a Pod without an OwnerReference. Optional
`?namespace=<ns>` narrows the scope. See
[Orphans & cycles](./concepts/orphan-cycle.md).

### `GET /api/v1/cycles`

Returns every strongly connected component of size ≥ 2 (Tarjan).
Empty on a healthy cluster.

## RBAC graph

### `GET /api/v1/rbac/serviceaccount/{namespace}/{name}/permissions`

Walks `BINDS_SUBJECT` incoming edges on the SA back through
RoleBinding / ClusterRoleBinding to the bound role, and returns
each role's rules block.

### `GET /api/v1/rbac/role/{namespace}/{name}/subjects`

Walks `BINDS_ROLE` incoming edges from a namespaced Role back to
the subjects each binding lists.

### `GET /api/v1/rbac/clusterrole/{name}/subjects`

Same shape, for cluster-scoped ClusterRoles. Split from the
namespaced variant because net/http's mux folds repeated
slashes.

## Search

### `GET /api/v1/search`

Linear case-insensitive substring scan over kind, name, namespace,
and labels. Capped at 200 results. Inverted-index replacement is
a v1.0.x candidate.

| Query param | Required | Meaning |
|---|---|---|
| `q` | yes | Search term (case-insensitive) |
| `limit` | no | Max matches (default 50, max 200) |

```bash
curl -s 'http://localhost:8080/api/v1/search?q=customers&limit=10'
```

## WebSocket watch

### `GET /api/v1/watch`

Upgrades to a WebSocket. The client sends one `SUBSCRIBE` envelope
to set its filter; the server then streams `GRAPH_UPDATE` envelopes
plus a `PING` heartbeat every 30 seconds.

Envelope shape (both directions):

```json
{
  "type": "SUBSCRIBE | UNSUBSCRIBE | GRAPH_UPDATE | PING | PONG",
  "level": "cluster | namespace | workload | resource",
  "namespace": "...",
  "kind": "...",
  "name": "...",
  "patch": { },
  "revision": 0
}
```

Filter semantics:

- `cluster` — receive every update.
- `namespace` — receive updates whose namespace matches, plus
  cluster-scoped updates.
- `workload` / `resource` — receive only updates whose
  (namespace, kind, name) triple matches.

Re-sending a `SUBSCRIBE` on an open connection narrows or broadens
the filter without a reconnect. The Web UI uses this when navigating
into a single-resource page.

## OpenAPI

### `GET /api/v1/openapi.json` and `GET /api/v1alpha1/openapi.json`

The full OpenAPI 3.0 document for each surface, generated from
the same `Routes()` table the server uses to register handlers —
so this page and the OpenAPI document can't drift independently.
The v1 spec includes the GA-only enrichment fields; the v1alpha1
spec is the frozen v0.1.0 shape.

## Stability

`/api/v1alpha1` is the frozen v0.1.0 contract. CI's
`api-compat-check` job runs on every PR and fails the build on
any path/schema/property removal, type change, or required-set
narrowing — see
[API versioning](./concepts/api-versioning.md) for the full
rules and the deprecation timeline.

`/api/v1/*` is the GA surface added in v1.0. From v1.0 onward,
semver applies: additive changes are minor-version events;
breaking changes are major-version events.

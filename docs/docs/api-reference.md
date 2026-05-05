---
sidebar_position: 4
title: API reference
---

# API reference

KubeAtlas serves a small REST API on port 8080 (proxied through the
`kubeatlas` Service when installed via Helm). The same Go binary
serves both the CLI and the API; the schema below is the v0.1.0
contract.

The **canonical** description is the OpenAPI 3.0 document the server
emits at `/api/v1alpha1/openapi.json`. This page is a hand-written
companion that's easier to skim — if it ever disagrees with the
OpenAPI document, trust the OpenAPI document.

## Conventions

- All paths return `Content-Type: application/json` unless noted.
- Cluster-scoped resources use `_` as the namespace placeholder in
  path templates (e.g. `/api/v1alpha1/resources/_/Namespace/default`).
- 4xx errors return `{ "error": "<message>" }`; 5xx errors return the
  same shape, with the message suitable for surfacing to operators.
- The API does not paginate. Every list endpoint returns the full
  result; the search endpoint caps at `limit=200`.

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

### `GET /api/v1alpha1/graph`

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
curl -s 'http://localhost:8080/api/v1alpha1/graph?level=cluster'

# Namespace view — one node per workload.
curl -s 'http://localhost:8080/api/v1alpha1/graph?level=namespace&namespace=petclinic'

# Workload view — the workload plus its referenced resources (BFS via OWNS).
curl -s 'http://localhost:8080/api/v1alpha1/graph?level=workload&namespace=petclinic&kind=Deployment&name=customers'

# Single-resource view — resource + one-hop neighbours, capped at 30.
curl -s 'http://localhost:8080/api/v1alpha1/graph?level=resource&namespace=petclinic&kind=Pod&name=customers-xxx'
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

The eight edge types are: `OWNS`, `USES_CONFIGMAP`, `USES_SECRET`,
`MOUNTS_VOLUME`, `SELECTS`, `USES_SERVICEACCOUNT`, `ROUTES_TO`,
`ATTACHED_TO`.

## Resource detail

### `GET /api/v1alpha1/resources/{namespace}/{kind}/{name}`

Returns the resource plus its incoming and outgoing edges in one
round-trip. Use `_` for cluster-scoped resources.

```bash
curl -s 'http://localhost:8080/api/v1alpha1/resources/petclinic/Deployment/customers' | jq
```

Response shape (`ResourceDetailResponse`):

```json
{
  "resource": { "id": "...", "kind": "Deployment", "namespace": "petclinic", "name": "customers", "labels": {...}, "annotations": {...}, "raw": {...} },
  "incoming": [ { "from": "...", "to": "...", "type": "OWNS" } ],
  "outgoing": [ { "from": "...", "to": "...", "type": "USES_CONFIGMAP" } ]
}
```

### `GET /api/v1alpha1/resources/{namespace}/{kind}/{name}/incoming`

Just the incoming edges — `from` is the other end, `to` is this
resource. Same path conventions as above.

### `GET /api/v1alpha1/resources/{namespace}/{kind}/{name}/outgoing`

Just the outgoing edges.

## Search

### `GET /api/v1alpha1/search`

Linear case-insensitive substring scan over kind, name, namespace,
and labels. v0.1.0 doesn't index — Phase 2 (v1.0) replaces this with
an inverted index. Capped at 200 results.

| Query param | Required | Meaning |
|---|---|---|
| `q` | yes | Search term (case-insensitive) |
| `limit` | no | Max matches (default 50, max 200) |

```bash
curl -s 'http://localhost:8080/api/v1alpha1/search?q=customers&limit=10'
```

## WebSocket watch

### `GET /api/v1alpha1/watch`

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

### `GET /api/v1alpha1/openapi.json`

The full OpenAPI 3.0 document for the surface above, generated from
the same `Routes()` table the server uses to register handlers — so
this page and the OpenAPI document can't drift independently.

## Stability

`/api/v1alpha1` is the v0.1.0 contract. From v0.1.0 onward, semver
applies: a field added to `Resource` or `Edge` is a minor-version
event; renaming or removing one is a major-version event. See the
[roadmap](./roadmap.md#compatibility-promises) for the full
guarantees.

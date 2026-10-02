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
- The API does not provide cursor pagination. Search and impact analysis have
  explicit result limits; impact truncation and errors are documented below.
- The examples below use `/api/v1/` URLs. Swap the prefix to
  `/api/v1alpha1/` only for shared legacy routes. Explicitly v1-only additions,
  including impact analysis, have no v1alpha1 counterpart.

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

Prometheus text exposition. Hand-rolled (no client_golang dependency); covers
the Go runtime memory boundary, goroutine count, informer and dependency
state, bounded operational counters, and request counts by method and status.
`Content-Type: text/plain`.

### `GET /api/v1/info`

Server build metadata plus the internal GraphStore interface
version. v1-only (not part of the frozen v1alpha1 surface).

```json
{ "version": "v1.5.0", "commit": "…", "build_date": "…", "graphstore_version": "v2" }
```

`graphstore_version` reports the internal GraphStore *engineering*
interface version (currently `v2`) — deliberately distinct from the
product/release version and from the HTTP API versions
(`v1alpha1`/`v1`); bumping it carries no public-API or release
meaning.

## Graph queries

### `GET /api/v1/graph`

Returns an aggregated `View` at one of four levels.

| Query param | Required | Meaning |
|---|---|---|
| `level` | yes | One of `cluster`, `namespace`, `workload`, `resource` |
| `namespace` | for `namespace`/`workload`/`resource` | Target namespace |
| `kind` | for `workload`/`resource` | Resource Kind (e.g. `Deployment`) |
| `name` | for `workload`/`resource` | Resource name |
| `label.<key>` | no | Label filter. Repeatable — every `label.<key>=<value>` must match. Honoured at `cluster` and `namespace` level; an edge counts only between two resources that both match. |

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

The fifteen built-in edge types are:

| Edge type | Source → Target | Added in |
|---|---|---|
| `OWNS` | Owned resource → owner (Pod → ReplicaSet → Deployment) | v0.1.0 |
| `USES_CONFIGMAP` | Workload → ConfigMap (envFrom, env.valueFrom, volumes) | v0.1.0 |
| `USES_SECRET` | Workload / ServiceAccount / Ingress / Gateway → reference-only Secret placeholder (env, volumes, pull secrets, TLS certificates) | v0.1.0 |
| `MOUNTS_VOLUME` | Workload → PersistentVolumeClaim | v0.1.0 |
| `SELECTS` | Service → Pod (via spec.selector) | v0.1.0 |
| `USES_SERVICEACCOUNT` | Workload → ServiceAccount | v0.1.0 |
| `ROUTES_TO` | Ingress / HTTPRoute / Route → Service | v0.1.0 |
| `ATTACHED_TO` | PersistentVolumeClaim → PersistentVolume | v0.1.0 |
| `BINDS_SUBJECT` | RoleBinding / ClusterRoleBinding → ServiceAccount / User / Group | v1.0.0 |
| `BINDS_ROLE` | RoleBinding / ClusterRoleBinding → Role / ClusterRole | v1.0.0 |
| `SELECTS_NP` | NetworkPolicy → Pod (via spec.podSelector) | v1.1.0 |
| `ALLOWS_FROM` | NetworkPolicy → ingress peer | v1.1.0 |
| `ALLOWS_TO` | NetworkPolicy → egress peer | v1.1.0 |
| `BINDS_PLATFORM_IDENTITY` | ServiceAccount → ExternalIdentity (EKS IRSA / AKS Workload Identity / GKE Workload Identity) | v1.3.0 |
| `SCALES` | HorizontalPodAutoscaler → workload (spec.scaleTargetRef) | v1.3.0 |

Additional types come from any loaded
[Rego rule packs](./concepts/rego-rules.md).

## Impact analysis (v1.7, unreleased)

This endpoint is implemented in the development tree, **not shipped in v1.6.0**.
Web/CLI/report integration and real-environment candidate acceptance remain
pending. It does not replace legacy blast-radius or snapshot/diff routes yet.

### `GET /api/v1/impact/{namespace}/{kind}/{name}`

Explains observed dependencies/dependents within one authorized cluster, with
separate authorization associations and explicit observation limitations.
`_` identifies a cluster-scoped root; the root namespace is not a traversal filter.

| Parameter | Meaning |
| --- | --- |
| `cluster` | Required single attached cluster in federation; omit in standalone |
| `relation` | `dependents` (default) or `dependencies` |
| `max_depth` | Default 5, range 1–10 |
| `limit` | Default 200, range 1–1,000, applied separately to the two facets |
| `expected_uid` | Optional observed root-instance precondition; unverifiable/mismatched UID returns 409 |

Repeated, empty, unknown, and invalid parameters return 400. Cluster visibility
is checked before graph/evidence reads. If visibility rules are configured,
missing tokens return 401 and denied cluster requests return 403. Without rules,
the existing all-visible policy applies; protect the service with external
authentication. Authorization here does not retrofit the legacy snapshot APIs.

```bash
# Standalone, locally authenticated/protected service as appropriate.
curl -s 'http://localhost:8080/api/v1/impact/demo/ConfigMap/settings?relation=dependents&limit=200'
```

The JSON envelope contains `schemaVersion`, `kubeatlasVersion`, `generatedAt`,
`scope`, `analysis`, and `sharingWarning`. Analysis contains root identity,
resources/counts/representative paths, reference explanations, a separate
`authorization` facet, `observation`, and `availability`. The served OpenAPI
documents all nested fields. Secret targets stay reference-only, with unknown
existence/UID/value; relationships do not prove an outage or effective permission.
Empty/incomplete observations never mean safe to delete. Historical data remains
`not_queried` unless the separate retained-metadata observer has checked it;
a successful current analysis is not a history or backup guarantee.

The ordinary observation's `before.sources` and `after.sources` include available
source-scoped records from standalone CRD and Gatekeeper discovery. They cover
only registered resource types, not a complete API inventory. Missing or
federated dynamic-source evidence remains unavailable; `inventoryLimited` marks
an exhausted evidence budget without disabling collection. Source changes across
the graph read invalidate the ordinary observation window's stability.
CRD endpoint/kind/scope/UID changes retire the previous registration; same-GVR
replacement advances continuity rather than reusing its callback token. Stopped
version records and known data gaps remain visible. Losing all served versions
stops collection; a delayed old-UID tombstone cannot stop its replacement.
Retiring a CRD registration retains graph data, not proof of current existence
or version equivalence. None of this establishes complete collector coverage.
Gatekeeper's optional `discovery` record describes only the supported v1
ConstraintTemplate API, with `state`, `checkedAt`, `revision`, and `stale`.
`advertised` does not prove controller health or list/watch permission;
`not_advertised` does not prove Gatekeeper is uninstalled. `unknown`,
`permission_denied`, and `failed` remain distinct. Cached discovery becomes
stale after ten minutes without declaring healthy watches disconnected; the
impact query never refreshes it. None of these states closes ordinary type scope
or converts an empty result into complete zero impact.

The ordinary captures also carry `apiInventory` from bounded background API
discovery in standalone mode. It describes endpoint metadata and all advertised
versions, with `unknown`, `complete`, `partial`, `permission_denied`, or `failed`
state, check time, generation/revision, stale/stopped/limited flags, and at most
256 descriptors. Discovery completeness does not imply graph observation.
`optionalApis` reports seven supported optional resources as `advertised`,
`not_advertised`, `other_version_advertised`, `unsupported_shape`, or `unknown`
before/after the read. Negative assessment requires a fresh complete inventory
and checks every advertised version; it does not assert operator installation.
`unobservedApis` names list/watch endpoints without captured registration
evidence, excluding core Secret endpoints. Inventory changes invalidate the
ordinary window; these fields leave authorization coverage independent.
Federated members currently have no production inventory worker, so their
inventory stays unavailable. The query reads cached evidence only.

With standalone Tier 2 history enabled, `availability.evidence.history` also
includes `retentionEvidence`: a five-minute background check of retained
metadata, expiring after ten minutes. Its state is `not_queried`, `observed`,
`failed`, or `unsupported`, with `checkedAt`, `stale`, `stopped`, and `bounds`.
The bounds identify the exact cluster and checked `from`/`to` window, first/last
non-Secret event times, and latest snapshot-marker time. Zero record timestamps
mean no matching retained records in that window, not no historical changes.
The parent `retainedData` becomes `observed` or `unavailable` as appropriate;
freshness and stopped flags still qualify a cached success. Each storage read
has a five-second deadline and returns no object identities, payloads, or counts.
The HTTP request performs no history query. Retention bounds do not establish
continuous history, and marker existence does not verify a restore point or
backup. Writer gaps and history coverage remain independent. This cache is not
exposed for federated members, disabled history, or Tier 1.

Depth/result limits can return a successful result with explicit truncation and
lower-bound counts. The **combined encoded response**, including both facets and
evidence, must fit 2 MiB; size exhaustion returns 413 without a partial success.
The request processing budget is 5 seconds; timeout attempts 504, but an expired
or disconnected socket may not deliver it. At most two impact requests run per
server; saturation returns 429. Unknown authorized roots/clusters return 404,
unsupported evidence/store capabilities return 503, and internal failures return
500. Errors contain a stable `code` and a sanitized `error` message.

Responses use `Cache-Control: no-store`, vary on Authorization, and carry a
warning about sensitive topology. See the [development contract](./development/impact-analysis.md)
for current coverage gaps and remaining acceptance work.

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

## NetworkPolicy

These endpoints expose the `SELECTS_NP` / `ALLOWS_FROM` / `ALLOWS_TO`
edges KubeAtlas derives from `NetworkPolicy` objects. They describe
the policy's **declared** topology — what the spec permits — not
what a CNI actually enforces on the wire.

### `GET /api/v1/networkpolicy/{namespace}/{name}/selected`

Returns every Pod (and Pod-template-carrying workload) in the
policy's namespace that `spec.podSelector` matches, resolved from
the `SELECTS_NP` edges. An empty `podSelector` selects every Pod in
the namespace.

### `GET /api/v1/networkpolicy/{namespace}/{name}/allow-graph`

Returns the `ALLOWS_FROM` (`spec.ingress[].from[]`) and
`ALLOWS_TO` (`spec.egress[].to[]`) targets — the Pods, workloads,
and Namespaces the policy declares as permitted peers.

## Snapshots (Tier 2)

Historical-snapshot endpoints. **Tier 2 only** — on a Tier 1
install, or Tier 2 with `snapshots.enabled=false`, every endpoint
below returns `503 Service Unavailable` with error code
`UNAVAILABLE`. See [Historical snapshots](./concepts/snapshots.md)
for the design.

### `GET /api/v1/snapshots`

Lists the recorded full-sync snapshot markers, most-recent first.

```json
{
  "snapshots": [
    { "timestamp": "2026-05-16T09:00:00Z", "resourceCount": 1432, "trigger": "periodic" }
  ],
  "count": 1
}
```

### `GET /api/v1/snapshots/diff`

Replays the event stream across a time window and classifies every
resource into `added` / `removed` / `modified`. The window may not
exceed the configured retention.

| Query param | Required | Meaning |
|---|---|---|
| `from` | yes | Window start: `now`, a duration read as "ago" (`5m`/`1h`/`7d`), or an RFC3339 timestamp |
| `to` | no | Window end; same formats, defaults to `now` |
| `namespace` | no | Restrict the diff to one namespace; empty diffs the whole cluster |

`400` if `from` is missing or unparseable, if `from` is not before
`to`, or if the window is wider than retention.

```bash
curl -s 'http://localhost:8080/api/v1/snapshots/diff?from=1h&to=now' | jq .
```

### `POST /api/_internal/snapshot/trigger`

Writes one `snapshot_meta` marker anchoring the diff endpoint to a
known full-sync point. **Internal** — served only on the ClusterIP
Service, never exposed through Ingress. The periodic full-sync
`CronJob` is the intended caller; `kubeatlas snapshot trigger`
reaches it from the CLI.

| Query param | Required | Meaning |
|---|---|---|
| `trigger` | no | Marker kind: `periodic` or `manual` (default `manual`) |

## Search

### `GET /api/v1/search`

Ranked full-text search over name, kind, namespace, and label
values. On Tier 2 it runs as one GIN-indexed `tsvector` match; on
Tier 1 it is a linear scan and the response carries a `warning`
field saying so. Capped at 200 results.

`q` accepts free-text terms plus `kind:` / `namespace:` (or `ns:`)
filter tokens — e.g. `customers kind:Pod`.

| Query param | Required | Meaning |
|---|---|---|
| `q` | yes | Free-text terms and/or `kind:`/`namespace:` filter tokens |
| `limit` | no | Max matches (default 50, max 200) |

```bash
curl -s 'http://localhost:8080/api/v1/search?q=customers%20kind:Pod&limit=10'
```

## Labels

### `GET /api/v1/labels`

Returns every label key present on any resource, how many resources
carry it, and its most common values. Each key's value list is
capped (a high-cardinality key like `pod-template-hash` would
otherwise return thousands); `valueCount` reports the true distinct
total. This is the data the "group by label" picker is built from,
and the key/value vocabulary for the `label.<key>` filter on
[`/graph`](#get-apiv1graph).

```bash
curl -s 'http://localhost:8080/api/v1/labels' | jq .
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

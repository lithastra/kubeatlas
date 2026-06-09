---
sidebar_position: 3
title: Blast radius
---

# Blast radius

Blast radius is KubeAtlas's answer to *"if I delete or break this
resource, what else fails?"* It walks the dependency graph
backwards along **incoming** edges from a target resource and
returns every resource that depends on it, transitively.

The idea is operational, not mathematical. Before deleting a
ConfigMap, you want to know which Deployments mount it. Before
rotating a Secret, you want to know which Pods reference it.
Before deleting a Role, you want to know which ServiceAccounts
have permissions through it. Each of those questions is a
single API call.

![Topology canvas in blast-radius mode — the selected root glows, every node and edge outside the reachable subgraph dims to ~20%, the top banner reports direction and depth, the right panel lists affected resources by hop.](/img/topology-blast-radius.png)

The Web UI surfaces this in the topology canvas: click any node,
hit **↯ Show blast radius** in the right panel (or right-click for
the radial picker), and the canvas dims everything outside the
reachable subgraph. The bottom toolbar lets you change depth
(1 / 2 / 3 / 5 / ∞) and direction (↓ downstream, ↑ upstream,
↕ both) without leaving the mode; Esc returns to the normal
canvas with your selection intact.

## API

```bash
GET /api/v1/blast-radius/{namespace}/{kind}/{name}
```

Cluster-scoped resources use `_` as the namespace (the same
sentinel the resource-detail endpoint uses).

```bash
curl 'http://127.0.0.1:8080/api/v1/blast-radius/petclinic/ConfigMap/db-config' | jq .
```

```json
{
  "source":   { "kind": "ConfigMap", "namespace": "petclinic", "name": "db-config" },
  "affected": [
    { "kind": "Deployment", "namespace": "petclinic", "name": "api" },
    { "kind": "ReplicaSet", "namespace": "petclinic", "name": "api-7c4d" },
    { "kind": "Pod",        "namespace": "petclinic", "name": "api-7c4d-x2k9p" }
  ],
  "count": 3,
  "maxDepth": 5
}
```

### Query parameters

| Param | Default | Notes |
|---|---|---|
| `max_depth` | `5` | Path-length cap. Hard ceiling 10 — deeper graphs are almost always cyclic. |
| `edge_types` | empty (any) | Comma-separated allowlist of edge labels to follow. Useful for "trace only OWNS" or "trace only USES_CONFIGMAP". |
| `include_source` | `false` | Set to `true` to include the start resource itself in the result; useful when you want a single subgraph for rendering. |

## What "incoming" means

KubeAtlas edges encode *child → parent* / *consumer → resource*
direction:

| Edge | Direction | Reading |
|---|---|---|
| `OWNS` | child → owner | `Pod -OWNS-> ReplicaSet`: "Pod is owned by ReplicaSet" |
| `USES_CONFIGMAP` | consumer → ConfigMap | `Deployment -USES_CONFIGMAP-> ConfigMap` |
| `USES_SECRET` | consumer → Secret | same shape |
| `BINDS_SUBJECT` | binding → subject | `RoleBinding -BINDS_SUBJECT-> ServiceAccount` |

The `BlastRadius` query asks: *who points at me?* That is, follow
the arrows in reverse. So the blast radius of a ConfigMap is the
set of consumers; the blast radius of a Role is the set of
RoleBindings that bind it; and so on.

## Algorithm

Tier 1 (in-memory): a BFS over the adjacency map keyed by `to`,
bounded by `max_depth`. Cost is O(V+E) over the reachable
sub-graph; for typical clusters this is sub-millisecond.

Tier 2 (PostgreSQL + Apache AGE): a recursive CTE on the plain
`edges` table — both directions share two fixed SQL strings,
which lets pgx's per-connection statement cache + Postgres'
plan cache reuse the work across calls. The AGE vertex / edge
mirror is still maintained by the double-write Upsert path
(future graph-pattern queries can use it); reads bypass AGE
because the recursive-CTE plan is well-trodden in Postgres and
the per-call `cypher()` parse cost otherwise dominated wall
time on short queries.

Both backends share the same `Direction` enum and the same
`Traverse` interface method, so semantics never drift between
tiers.

Both implementations use a UID-keyed visited set for de-duping —
no marshal/unmarshal in the hot loop.

## Folded into resource detail (`/api/v1/...`)

The v1 GA surface adds three enrichment fields to the
resource-detail endpoint:

```bash
curl 'http://127.0.0.1:8080/api/v1/resources/petclinic/ConfigMap/db-config' | jq .
```

```json
{
  "resource": { "...": "..." },
  "incoming": [],
  "outgoing": [],
  "blastRadiusCount": 3,
  "isOrphan": false,
  "inCycle": false
}
```

`blastRadiusCount` is a single integer — the same number the
explicit endpoint returns. The UI uses this to render a badge on
each resource row without a follow-up round-trip.

The v1alpha1 surface (`/api/v1alpha1/...`) intentionally **does
not** carry these fields; the Phase 1 byte shape is frozen — see
[API versioning](api-versioning).

## What blast radius does not tell you

- **Whether the affected resources will actually break.** A
  Deployment that mounts a ConfigMap as `optional: true` survives
  the ConfigMap going missing. The graph encodes the dependency,
  not the failure mode.
- **Anything about runtime traffic.** The dependency is static
  (manifest-derived). For "what's actually receiving requests",
  pair this with a service-mesh / metrics tool.
- **Custom-resource semantics that aren't in a rule pack.** If
  your cluster has CRDs whose dependencies aren't covered by the
  built-in extractors or any loaded rule pack, those edges won't
  show up. See [Rego rules](rego-rules) for the extension point.

## Performance budget

The playbook target for v1.0 is **p95 < 500ms** on a 5K-resource
cluster, both Tier 1 and Tier 2. The unit-test perf gate
(`pkg/graph/analysis/cycles_test.go` + the Tier 2 traverse
benchmark in `pkg/store/postgres`) keep both backends honest
during development. The capture-once baseline lives at
`test/verify/perf-baseline-v1.0.json`; CI does not run it on
every PR (cost), but `bash test/perf/bench-v1.sh` can be
triggered before any tag cut.

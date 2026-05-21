---
sidebar_position: 3
title: Architecture
---

# Architecture

This page summarises the design as it stands at v1.2.0 (with the
v1.3 multi-cluster federation data layer landed in main). The
Headlamp plugin shipped in v1.1; multi-cluster federation lands in
v1.3 — its data layer + `/api/v1/federation/*` endpoints in v1.3.0,
the Web UI cluster switcher in v1.3.1. Anything else flagged
"deferred" inline below is called out at the place it matters.

## Six design principles

1. **Read-only, always.** KubeAtlas never modifies cluster state — no
   create, update, patch, or delete in the RBAC manifest, ever. The
   moment that promise stops being true, the threat model changes
   completely.
2. **Offline-friendly.** The graph is built from data the cluster
   already exposes; no external services are contacted at runtime, no
   telemetry is reported, no API keys are needed.
3. **Zero-config by default, persistent on demand.** Tier 1 storage
   is in-memory and remains the default for first-install simplicity.
   Tier 2 (PostgreSQL + Apache AGE) is opt-in via one Helm flag and
   uses the embedded CloudNativePG sub-chart for a single-command
   install.
4. **CRD-friendly.** The discovery layer is GVR-driven, with dynamic
   CRD discovery from v1.0 — new CRDs become per-CRD informers at
   runtime, and a [Rego rule pack](./concepts/rego-rules.md) can
   teach the graph their edges without a rebuild.
5. **Two form factors, one engine.** The same Go binary serves the
   CLI (`-once` mode, `export` subcommand) and a long-running server
   with REST + WebSocket endpoints. The Web UI consumes those
   endpoints.
6. **Pre-aggregate on the server.** Cluster-, namespace-, workload-,
   and resource-level views are computed server-side. Clients receive
   ready-to-render JSON instead of having to traverse the full graph.

## How the pieces fit together

```
                         ┌────────────────────────────┐
                         │     Kubernetes apiserver   │
                         └────────────┬───────────────┘
                                      │ watch / list
                                      ▼
              ┌───────────────────────────────────────────┐
              │  pkg/discovery (informer + GVR registry)  │
              └────────────┬─────────────────────┬────────┘
                           │ resources           │ raw events
                           ▼                     ▼
              ┌──────────────────────┐  ┌──────────────────────┐
              │  pkg/extractor       │  │  pkg/graph           │
              │  (8 edge types)      │──▶  GraphStore (Tier 1) │
              └──────────────────────┘  └──────────┬───────────┘
                                                   │ snapshot
                                                   ▼
                                        ┌──────────────────────┐
                                        │  pkg/aggregator      │
                                        │  (cluster, ns, ...)  │
                                        └──────────┬───────────┘
                                                   │ JSON
                                                   ▼
                                ┌────────────────────────────────┐
                                │  CLI (-once / export)  /  REST │
                                │   /api/v1alpha1/* + /api/v1/*  │
                                └────────────────────────────────┘
```

From v1.0 the GraphStore interface has a Tier 2 implementation
backed by PostgreSQL + Apache AGE in `pkg/store/postgres`. Reads
that need graph traversal (blast-radius, orphan/cycle detection)
go through a recursive CTE on the `edges` table; vertex + edge
writes are double-written to both the SQL tables and the AGE
graph so future graph-pattern queries can use the latter. CRD
discovery is dynamic — `pkg/crd` walks the cluster's CRD list,
registers per-CRD informers at runtime, and routes their events
through the Rego rule pack engine in `pkg/extractor/rego`.

### Data acquisition (`pkg/discovery`)

A `dynamicinformer.SharedInformerFactory` watches the resources in
`CoreGVRs`. Optional API groups (`gateway.networking.k8s.io`) are
filtered out at startup so KubeAtlas runs cleanly on clusters where
Gateway API is not installed. Add/update/delete events are translated
into typed `graph.Resource` values and forwarded to the store.

### Graph engine (`pkg/graph` + `pkg/store/memory`)

`graph.GraphStore` is the persistence-agnostic interface — Upsert,
Delete, Get, List, Snapshot. The default backend is an in-memory map
guarded by a single `RWMutex`. Edge identity is the
`(from, to, type)` triple, so two different edge types between the
same pair of resources coexist (for example, a Service that both
`SELECTS` and `ROUTES_TO` the same Pod).

A `storetest.Run(t, factory)` suite locks down the contract: any
backend that passes it is a drop-in replacement.

### Edge extraction (`pkg/extractor` + `pkg/extractor/rego`)

Ten built-in edge types cover the core Kubernetes resources:

| Type | Source field |
|---|---|
| `OWNS` | `metadata.ownerReferences` |
| `USES_CONFIGMAP` | `envFrom.configMapRef`, `valueFrom.configMapKeyRef`, `volumes[].configMap` |
| `USES_SECRET` | `envFrom.secretRef`, `valueFrom.secretKeyRef`, `volumes[].secret` |
| `MOUNTS_VOLUME` | `volumes[].persistentVolumeClaim.claimName` |
| `SELECTS` | `Service.spec.selector` matched against Pod labels |
| `USES_SERVICEACCOUNT` | `spec.template.spec.serviceAccountName` (or implicit `default`) |
| `ROUTES_TO` | `Ingress.spec.rules[].http.paths[].backend.service.name`, `HTTPRoute.spec.rules[].backendRefs[].name` |
| `ATTACHED_TO` | `HTTPRoute.spec.parentRefs[].name` |
| `BINDS_SUBJECT` | `RoleBinding`/`ClusterRoleBinding` → subject (ServiceAccount, User, Group) |
| `BINDS_ROLE` | `RoleBinding`/`ClusterRoleBinding` → bound `Role`/`ClusterRole` |

Built-in extractors are stateless and never call back into the
store — the informer is responsible for writing what they return.
Additional edge types come from any loaded
[Rego rule packs](./concepts/rego-rules.md), which run inside a
sandbox (`evaluateWithGuards` — 100 ms eval timeout, panic
recovery) and write through the same Upsert path.

### Aggregation (`pkg/aggregator`)

Pre-aggregation produces ready-to-render summaries:

- **Cluster level** — one node per namespace with `children_count` and
  a `children_summary` of resource kinds.
- **Namespace level** — one node per workload (Deployment, StatefulSet,
  DaemonSet, Job, CronJob, Service, Ingress) inside the namespace.
- **Workload / Resource levels** — single-workload + one-hop
  neighbour views; the resource level powers the Web UI's
  resource-detail page.

This shape lets the Web UI render a useful overview without ever
materialising the full graph in the browser.

### Graph analysis (`pkg/graph/analysis`)

Three composed queries that share the `Direction` enum on the
`GraphStore.Traverse` interface method:

- **Blast radius** — `Traverse(Direction=Incoming, MaxDepth=5)`
  returns the transitive set of resources affected by a target.
  See [Blast radius](./concepts/blast-radius.md).
- **Orphans** — `Snapshot` + per-resource `ListIncoming`, applying
  the top-level whitelist + standalone-Pod special case.
- **Cycles** — Tarjan's SCC on the edges table; returns every SCC
  of size ≥ 2.

## What v1.0 ships on top of the engine

- **`pkg/api`** — REST endpoints for graph queries (`GET
  /api/v1/graph` at four levels), single-resource detail with v1
  enrichment fields, search, RBAC graph, blast-radius, orphans,
  cycles, health / readiness / metrics, WebSocket watch. The
  frozen `/api/v1alpha1/*` surface is served from the same
  handlers — see [API versioning](./concepts/api-versioning.md).
- **`pkg/store/postgres`** — Tier 2 backend on PostgreSQL ≥ 14
  with the Apache AGE extension. Migration framework, double-
  write Upsert, recursive-CTE traversal. Embedded mode uses the
  CloudNativePG sub-chart with auto-provisioned credentials.
- **`pkg/extractor/rego`** — OPA SDK v1 (`v1/rego` import path)
  with module loading, GVK routing, an `(UID, ResourceVersion,
  RuleHash)`-keyed LRU cache, and the `evaluateWithGuards`
  sandbox (100 ms timeout + panic recovery). Loads rule packs
  from local directories or signed OCI artifacts.
- **`pkg/crd`** — dynamic CRD discovery + OpenShift detector +
  embedded openshift rule pack.
- **`web/`** — React 19 + TypeScript + MUI v5 Web UI. Cytoscape
  topology view at cluster / namespace / workload levels; the
  resource-detail page renders the v1 enrichment fields as
  badges. Mermaid neighbour view stays for backward compat
  alongside the Cytoscape views.
- **`helm/`** — installable chart with secure defaults baked in:
  ClusterIP-only Service, Ingress disabled by default, a Helm
  `values.schema.json` gate that requires explicit
  `acknowledgeNoBuiltinAuth=true` before exposing KubeAtlas, an
  RBAC ClusterRole hard-coded to `[get, list, watch]`, a Pod
  that runs as non-root with a read-only root filesystem, opt-in
  Tier 2 persistence, opt-in cert-manager TLS integration.
- **Distribution** — multi-arch container image on
  `ghcr.io/lithastra/kubeatlas`, four-platform binaries, Helm Chart
  published as an OCI artifact at
  `oci://ghcr.io/lithastra/charts/kubeatlas`, cosign-signed,
  SBOM-attached.

For where KubeAtlas is going next — multi-cluster federation
(v1.3, the final Phase 3 release) and cloud-resource integration
(beyond Phase 3) — see the [Roadmap](./roadmap.md).

The v0.1.0 API surface and the `graph.Resource`/`graph.Edge`
shapes stay frozen across v1.x: only additive changes. CI's
`api-compat-check` enforces this on every PR.

---
sidebar_position: 3
title: Architecture
---

# Architecture

This page summarises the design as it stands in the v1.5 release
line. v1.4 added offline diagnostics, admission-policy visibility,
and opt-in anonymous telemetry. v1.5 added the opt-in OpenTelemetry
runtime overlay, read-side multi-cluster visibility controls, and
the internal GraphStore v2 contract. The public `v1alpha1` surface
remains frozen and `/api/v1` changes remain additive.

## Six design principles

1. **Read-only, always.** KubeAtlas never modifies cluster state — no
   create, update, patch, or delete in the RBAC manifest, ever. The
   moment that promise stops being true, the threat model changes
   completely.
2. **Offline-friendly by default.** The graph is built from data the
   cluster already exposes; the default runtime needs no external
   service, telemetry, or API key. Anonymous usage telemetry and OCI
   rule-pack downloads are explicit opt-ins with separately documented
   network and trust boundaries.
3. **Zero-config by default, persistent on demand.** Tier 1 storage
   is in-memory and remains the default for first-install simplicity.
   Tier 2 (PostgreSQL + Apache AGE) is opt-in via one Helm flag and
   uses a namespaced `Cluster` reconciled by the separately installed,
   cluster-scoped CloudNativePG operator.
4. **CRD-friendly without cross-API wildcard access.** The discovery layer is
   GVR-driven and watches CRD definitions at runtime. A CRD becomes readable
   only when the ServiceAccount has an explicit read-only grant for that
   non-core API group. The chart supplies exact grants for its cert-manager,
   CloudNativePG, Kyverno, and Gatekeeper integrations; a
   [Rego rule pack](./concepts/rego-rules.md) can then teach the graph its edges
   without a rebuild.
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
                           │ resources           │ metadata events
                           ▼                     ▼
              ┌──────────────────────┐  ┌──────────────────────┐
              │  pkg/extractor       │  │  pkg/graph           │
              │  (built-in + Rego)   │──▶  GraphStore (Tier 1) │
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
graph so future graph-pattern queries can use the latter. CRD discovery is
dynamic — `pkg/crd` walks the cluster's CRD list and attempts to register
per-CRD informers. The Kubernetes API server delivers objects only when the
KubeAtlas ServiceAccount has an explicit read-only grant for the CR's API
group; otherwise that informer remains unauthorised. Events that are delivered
flow through the Rego rule pack engine in `pkg/extractor/rego`.

### Data acquisition (`pkg/discovery`)

A `dynamicinformer.SharedInformerFactory` watches the resources in
`CoreGVRs`. Optional API groups (`gateway.networking.k8s.io`) are
filtered out at startup so KubeAtlas runs cleanly on clusters where
Gateway API is not installed. Add/update/delete events are translated
into typed `graph.Resource` values and forwarded to the store.

Core/v1 Secrets are deliberately absent from `CoreGVRs` and the Helm
ClusterRole. Kubernetes RBAC cannot return metadata-only Secret objects.
Instead, extractors derive Secret names from non-Secret references and create
nodes marked `kubeatlas.io/reference-only=true`. Those nodes prove only that a
reference was observed; KubeAtlas cannot confirm that the Secret exists.

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

Sixteen declarative edge types cover core Kubernetes resources and
the policy/platform relationships discovered by the built-in engine:

| Type | Source field |
|---|---|
| `OWNS` | `metadata.ownerReferences` |
| `USES_CONFIGMAP` | `envFrom.configMapRef`, `valueFrom.configMapKeyRef`, `volumes[].configMap` |
| `USES_SECRET` | Workload env/volume/image-pull refs, ServiceAccount refs, Ingress TLS, Gateway certificate refs |
| `MOUNTS_VOLUME` | `volumes[].persistentVolumeClaim.claimName` |
| `SELECTS` | `Service.spec.selector` matched against Pod labels |
| `USES_SERVICEACCOUNT` | `spec.template.spec.serviceAccountName` (or implicit `default`) |
| `ROUTES_TO` | `Ingress.spec.rules[].http.paths[].backend.service.name`, `HTTPRoute.spec.rules[].backendRefs[].name` |
| `ATTACHED_TO` | `HTTPRoute.spec.parentRefs[].name` |
| `BINDS_SUBJECT` | `RoleBinding`/`ClusterRoleBinding` → subject (ServiceAccount, User, Group) |
| `BINDS_ROLE` | `RoleBinding`/`ClusterRoleBinding` → bound `Role`/`ClusterRole` |
| `SELECTS_NP` | `NetworkPolicy.spec.podSelector` → selected Pod/workload |
| `ALLOWS_FROM` | `NetworkPolicy.spec.ingress[].from[]` → declared source |
| `ALLOWS_TO` | `NetworkPolicy.spec.egress[].to[]` → declared destination |
| `BINDS_PLATFORM_IDENTITY` | ServiceAccount → cloud workload identity |
| `SCALES` | `HorizontalPodAutoscaler.spec.scaleTargetRef` → workload |
| `ENFORCES` | Gatekeeper/Kyverno policy → affected resource |

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
`GraphStore.ListReachable` interface method:

- **Blast radius** — `ListReachable(Direction=Incoming, MaxDepth=5)`
  returns the transitive set of resources affected by a target.
  See [Blast radius](./concepts/blast-radius.md).
- **Orphans** — `Snapshot` + per-resource `ListEdges(…, DirectionIncoming)`, applying
  the top-level whitelist + standalone-Pod special case.
- **Cycles** — Tarjan's SCC on the edges table; returns every SCC
  of size ≥ 2.

## What v1.5 ships on top of the engine

- **`pkg/api`** — REST endpoints for graph queries (`GET
  /api/v1/graph` at four levels), single-resource detail with v1
  enrichment fields, search, RBAC graph, blast-radius, orphans,
  cycles, snapshots, diagnostics, admission policies, federation,
  the OTel runtime overlay, health / readiness / metrics, and
  WebSocket watch. The
  frozen `/api/v1alpha1/*` surface is served from the same
  handlers — see [API versioning](./concepts/api-versioning.md).
- **`pkg/store/postgres`** — Tier 2 backend on PostgreSQL ≥ 14
  with the Apache AGE extension. Migration framework, double-
  write Upsert, recursive-CTE traversal. Embedded mode uses an
  external CloudNativePG operator with auto-provisioned credentials.
- **`pkg/extractor/rego`** — OPA SDK v1 (`v1/rego` import path)
  with module loading, GVK routing, an `(UID, ResourceVersion,
  RuleHash)`-keyed LRU cache, and the `evaluateWithGuards`
  sandbox (100 ms timeout + panic recovery). Loads rule packs
  from local directories or signed OCI artifacts.
- **`pkg/crd`** — dynamic CRD discovery + OpenShift detector +
  embedded openshift rule pack.
- **`web/`** — React 19 + TypeScript + MUI v5 Web UI. v1.3 introduced
  the cartography redesign: a single full-bleed Cytoscape canvas
  with one persistent shell (`AtlasShell`) rather than per-route
  pages. Five runtime-switchable themes (Parchment / Survey /
  Terrain / Ink / Slate) sharing one CSS-variable contract. Modes
  fold into the canvas instead of replacing it: a ⌘K command
  palette (`/api/v1alpha1/search` + canvas match highlighting), a
  blast-radius BFS with depth + direction controls and a dim /
  brighten pass on the cytoscape elements, a time-axis diff with
  anchor presets (1h / 4h / 24h / 7d) that decorate added /
  removed / modified nodes from `/api/v1/snapshots/diff`, an
  edge-type filter chip (All / RBAC / Network / Config / Storage),
  a zoom-scale widget mapping cytoscape zoom × → L1–L4 bands, and
  a left cluster strip wired to `/api/v1/federation/clusters`.
  The resource-detail page still renders the v1 enrichment fields
  as badges and the Mermaid neighbour view stays alongside. v1.4
  adds the policy view; v1.5 adds the opt-in OTel overlay and trace
  timeline.
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
  `oci://ghcr.io/lithastra/charts/kubeatlas`. Release-specific
  signatures, SBOMs, and provenance are claimed only where the
  corresponding release workflow and public artifact provide
  verifiable evidence.

The planned v1.6 release line focuses on production operability: a
supported Kubernetes, CloudNativePG, and PostgreSQL baseline; one tested
v1.5.2 upgrade and embedded Tier 2 restore path; scheduled clean-cluster
evidence; performance and seven-day endurance gates; operator-visible
failure signals; and verifiable core OCI artifacts. It remains a
single-application-replica, externally authenticated, read-only deployment.
Generated clients are a non-blocking stretch goal and ecosystem packages keep
their independent release cadence. See the [v1.6 scope and release
gates](./roadmap.md#v16-planned--production-operability).

The v0.1.0 API surface and the `graph.Resource`/`graph.Edge`
shapes stay frozen across v1.x: only additive changes. CI's
`api-compat-check` enforces this on every PR.

---
sidebar_position: 3
title: Architecture
---

# Architecture

This page summarises the design that the Phase 0 codebase implements.
The REST API and Web UI sketched at the bottom land in Phase 1.

## Six design principles

1. **Read-only, always.** KubeAtlas never modifies cluster state — no
   create, update, patch, or delete in the RBAC manifest, ever. The
   moment that promise stops being true, the threat model changes
   completely.
2. **Offline-friendly.** The graph is built from data the cluster
   already exposes; no external services are contacted at runtime, no
   telemetry is reported, no API keys are needed.
3. **Zero data dependency at v0.1.0.** Tier 1 storage is in-memory.
   PostgreSQL + Apache AGE (Tier 2) is opt-in, and arrives in v1.0
   (see the [Roadmap](./roadmap.md)).
4. **CRD-friendly.** The discovery layer is GVR-driven. Adding a
   custom resource means appending one entry to the registry; the
   informer pipeline handles it like any other kind.
5. **Two form factors, one engine.** The same Go binary serves the
   CLI (`-once` mode) and — from Phase 1 — a long-running server with
   REST + WebSocket endpoints. The Web UI consumes those endpoints.
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
                                │  CLI (-once)  /  REST (Phase 1)│
                                └────────────────────────────────┘
```

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

### Edge extraction (`pkg/extractor`)

Eight built-in edge types cover the Phase 0 / v0.1.0 scope:

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

Extractors are stateless and never call back into the store — the
informer is responsible for writing what they return.

### Aggregation (`pkg/aggregator`)

Pre-aggregation produces ready-to-render summaries:

- **Cluster level** — one node per namespace with `children_count` and
  a `children_summary` of resource kinds.
- **Namespace level** — one node per workload (Deployment, StatefulSet,
  DaemonSet, Job, CronJob, Service, Ingress) inside the namespace.
- **Workload / Resource levels** — added in Phase 1 W5.

This shape lets the Web UI render a useful overview without ever
materialising the full graph in the browser.

## What lands in Phase 1 (preview)

- **`pkg/api`** — REST endpoints for graph queries (`GET
  /api/v1alpha1/graph` at four levels: cluster / namespace /
  workload / resource), a single-resource detail endpoint, search,
  health/readiness/metrics, and a WebSocket watch endpoint
  (`/api/v1alpha1/watch`). All served by the same binary.
- **`web/`** — React 19 + TypeScript + MUI v5 Web UI,
  technology-stack-aligned with Headlamp so a v1.0 Headlamp plugin
  is a port rather than a rewrite. Cytoscape topology view at the
  cluster, namespace, and workload levels; Mermaid neighbor view
  at the single-resource level; a DataGrid resource list with
  namespace filtering.
- **`helm/`** — installable chart with secure defaults baked in:
  ClusterIP-only Service, Ingress disabled by default, a Helm
  `values.schema.json` gate that requires explicit
  `acknowledgeNoBuiltinAuth=true` before exposing KubeAtlas, an
  RBAC ClusterRole hard-coded to `[get, list, watch]`, and a Pod
  that runs as non-root with a read-only root filesystem.
- **Distribution** — multi-arch container image on
  `ghcr.io/lithastra/kubeatlas`, four-platform binaries, Helm Chart
  published as an OCI artifact.

For the full Phase 1 plan plus what's deliberately out of scope for
v0.1.0, see the [Roadmap](./roadmap.md).

The Phase 0 code paths above stay frozen during Phase 1: only new
methods, new files, and new tests are added. No PoC-era field is
renamed and no signature is changed.

---
sidebar_position: 5
title: Roadmap
---

# Roadmap

This is a rough, **non-binding** plan for where KubeAtlas is going.
Dates slip, scopes change, and external feedback frequently reshapes
priorities. Treat this page as direction, not contract.

For the current state, see [What is KubeAtlas](./).

## Where we are

**v1.0.0 is released.** Install with
`helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas --version 1.0.0`
— see the [Quick Start](./quick-start.md). v1.0.x patch work is
now the active cycle; v1.1 themes are sketched below.

| Phase | Status | What it delivered |
|---|---|---|
| **Phase 0** (Foundation) | ✅ Done | CLI binary, in-memory graph, 8 edge types, 16 watched resources, contract-tested store interface, contributor docs, CI gates. No API, no UI, no Helm Chart. |
| **Phase 1** (MVP → v0.1.0) | ✅ Released | REST + WebSocket API, React/MUI Web UI with Cytoscape topology and Mermaid neighbour view, Helm Chart with secure defaults, Playwright E2E, multi-platform release. Available as `oci://ghcr.io/lithastra/charts/kubeatlas:0.1.0`. |
| **Phase 2** (v1.0) | ✅ Released | Tier 2 persistence (PostgreSQL + Apache AGE), Rego rule packs, RBAC graph, blast radius, orphan + cycle detection, `/api/v1/*` GA, cert-manager TLS, OpenShift detector + embedded pack, chaos test suite. Available as `oci://ghcr.io/lithastra/charts/kubeatlas:1.0.0`. |
| **Phase 3** (v2.0+) | 💭 Sketch | Multi-cluster, cloud integration. |

## Related tools

KubeAtlas overlaps with a few other projects in the Kubernetes
observability / introspection space. The honest position is that the
overlaps are partial — these tools answer adjacent questions, and most
teams will end up running more than one. This section explains the
deliberate scope choices.

### vs. [Karpor](https://github.com/KusionStack/karpor)

[Karpor](https://github.com/KusionStack/karpor) is a Kubernetes
search and insight platform from the KusionStack ecosystem. It
overlaps with KubeAtlas only at the surface — both look at K8s
objects and surface relationships — but the two projects make
fundamentally different choices about who they're for and what
they optimise.

KubeAtlas differs in three ways that matter:

1. **KubeAtlas is an independent tool.** It isn't part of a larger
   platform, doesn't assume an ecosystem, and doesn't impose a
   workflow. You install it, point it at a cluster, get answers.
   Adoption costs nothing beyond the binary. Removal is the same
   `helm uninstall`. No long-term commitment to a vendor or stack.

2. **KubeAtlas focuses on dependency analysis** — specifically, the
   first-glimpse problem an infrastructure engineer faces when
   handed an unfamiliar cluster: *"what's running here, and how is
   it wired together?"* The answers are derived as a typed
   dependency graph (eight edge kinds today, more later) so
   questions like *"if I delete this Secret, what breaks?"* are
   one traversal, not a series of greps. Search is a side benefit;
   structure is the point.

3. **KubeAtlas is small, compact, and easy to onboard and extend.**
   The default deploy is a single Pod with no external dependencies
   — no search backend, no message queue, no sidecars. Opt into
   Tier 2 persistence (PostgreSQL + Apache AGE via the embedded
   CloudNativePG sub-chart) when you're ready. Onboarding is
   `helm install` plus a port-forward. Extending the edge schema
   is either one Go file plus one test (see
   [Adding a new edge type](./developer-guide.md#adding-a-new-edge-type--a-worked-example))
   or a [Rego rule pack](./concepts/rego-rules.md) loaded at
   runtime from an OCI artifact — no rebuild required. The codebase
   is small enough that a contributor can read the full graph
   engine in an afternoon.

Karpor and KubeAtlas can coexist on the same cluster — they answer
different questions. If you need cross-cluster search, indexing,
and a centralized insight pipeline, Karpor is the right shape for
that. If you need to understand a single cluster's structure
right now with a tool you can install in five minutes and remove
just as easily, KubeAtlas is the right shape for that.

### Other tools you might evaluate alongside

A short list, with the question each is best at:

- **[Headlamp](https://headlamp.dev/) / [Lens](https://k8slens.dev/)** — "Show me everything in this cluster, navigably." General-purpose K8s UIs. KubeAtlas ships its own UI for the graph; a Headlamp plugin lives in `lithastra/kubeatlas-headlamp-plugin` and is tracked as a v1.1 target.
- **[`kubectl tree`](https://github.com/ahmetb/kubectl-tree)** — "Show owner-reference children of this object." Kubectl plugin, single-edge-type, terminal-only. KubeAtlas covers the same ground via the OWNS edge plus seven others, with a server and UI on top.
- **[Argo CD](https://argoproj.github.io/cd/)** — Resource topology, but framed around an Application as the root. KubeAtlas roots are arbitrary; you don't need GitOps adoption.
- **Prometheus / Grafana / DataDog** — Metric and event observability. Disjoint problem space.

If you have a tool that should be on this list, [open a doc PR](https://github.com/lithastra/kubeatlas/blob/main/CONTRIBUTING.md).

## Phase 1 → v0.1.0 (released)

The first publicly-releasable build. **Shipped on 2026-05-06.**
Install reference: see [Quick Start](./quick-start.md). Release
notes:
[github.com/lithastra/kubeatlas/releases/tag/v0.1.0](https://github.com/lithastra/kubeatlas/releases/tag/v0.1.0).

Delivered scope:

- **REST API** — `GET /api/v1alpha1/graph` at four levels
  (cluster / namespace / workload / resource), `GET /resources/{ns}/{kind}/{name}` for detail, `GET /search`, `/healthz`, `/readyz`, `/metrics`
- **WebSocket** — `/api/v1alpha1/watch` for live graph updates
- **Web UI** — React 19 + TypeScript + MUI v5, technology-stack-aligned with [Headlamp](https://headlamp.dev/) so a future Headlamp plugin (Phase 2) is a port rather than a rewrite
  - Cytoscape topology view (cluster / namespace / workload levels)
  - Mermaid neighbor view (single resource + one hop)
  - DataGrid resource list with namespace filter
- **Helm Chart** — `oci://ghcr.io/lithastra/charts/kubeatlas` with secure defaults baked in:
  - `service.type: ClusterIP` (no automatic LoadBalancer exposure)
  - Ingress disabled by default; enabling it requires explicit `acknowledgeNoBuiltinAuth=true` (schema-validated)
  - RBAC ClusterRole hard-coded to `[get, list, watch]` (KubeAtlas is read-only, always)
  - Pod runs as non-root with read-only root filesystem
- **Distribution** — multi-arch container image (linux/amd64, linux/arm64) on `ghcr.io/lithastra/kubeatlas`, four-platform binaries via goreleaser, Helm Chart published as OCI artifact
- **Docs site** — quick-start, installation guides per Ingress flavour, architecture, FAQ, this roadmap

### Explicitly *not* in v0.1.0

So v0.1.0 shipped something usable instead of trying to ship
everything:

- ❌ Built-in authentication — operators provide it via the Ingress layer (oauth2-proxy / Pomerium / Cloudflare Access; example values shipped)
- ❌ Persistence — Tier 1 in-memory only; restart loses graph state
- ❌ Multi-cluster — one kubeconfig per KubeAtlas instance
- ❌ Custom edge types — the eight built-in edges were it for v0.1.0
- ❌ Dynamic CRD discovery — the 16 watched GVRs were hard-coded
- ❌ RBAC graph and NetworkPolicy graph
- ❌ Historical snapshots / diff
- ❌ Dark mode
- ❌ Headlamp plugin

The first seven shipped in v1.0; dark mode + Headlamp plugin
moved to v1.1.

## Phase 2 → v1.0 (released)

The "make it suitable for production observability" cycle.
Shipped scope:

| Theme | What landed |
|---|---|
| **Persistence** | Tier 2 storage on PostgreSQL ≥ 14 with the [Apache AGE](https://age.apache.org/) extension. Opt-in via `persistence.enabled=true`; the embedded mode (`persistence.embedded.enabled=true`) ships [CloudNativePG](https://cloudnative-pg.io/) as a sub-chart. Restart now preserves the graph; informer cold-start drops to ~4 s reading the persisted state. |
| **Extensibility** | [Rego rule packs](./concepts/rego-rules.md) — declare CRD edges in Rego, no rebuild. Packs are OCI-distributed and signed. Embedded OpenShift pack auto-loads when `route.openshift.io` is detected; extras load via `rulePacks.extras`. Dynamic CRD discovery is built in — KubeAtlas walks the cluster's CRDs and registers per-CRD informers at runtime. |
| **More edge kinds** | [RBAC graph](./api-reference.md) — `BINDS_SUBJECT` and `BINDS_ROLE` edges plus three new endpoints (`/api/v1/rbac/serviceaccount/<ns>/<name>/permissions`, `/api/v1/rbac/role/<ns>/<name>/subjects`, `/api/v1/rbac/clusterrole/<name>/subjects`). |
| **Impact radius** | [Blast radius](./concepts/blast-radius.md) — `/api/v1/blast-radius/<ns>/<kind>/<name>` walks incoming edges and returns the affected set. Folded into the v1 resource-detail bundle as `blastRadiusCount`. |
| **Orphans + cycles** | [Detection](./concepts/orphan-cycle.md) — two new endpoints for stale resources and Tarjan-detected SCCs. |
| **API surface** | `/api/v1/*` GA endpoints, frozen `/api/v1alpha1/*` retained — see [API versioning](./concepts/api-versioning.md). |
| **TLS** | [cert-manager Helm integration](./installation/cert-manager.md) — selfsigned / letsencrypt-staging / letsencrypt-prod / custom. |
| **OpenShift** | [Auto-detect + install path](./installation/openshift.md) plus the weekly e2e-openshift-local (CRC) workflow. |
| **Performance** | 5K-resource perf bench + regression gate; recursive-CTE traversal so blast-radius p95 stays under 500 ms on a 7K-resource cluster. |
| **Chaos** | `test/chaos/` scenarios (pg-disconnect, rego-panic, rego-runaway, cert-manager-flap) gating the release. |

### Deferred to v1.1

These were in the original Phase 2 plan but moved out for the
v1.0 cut:

- Headlamp plugin (`lithastra/kubeatlas-headlamp-plugin`)
- Frontend Mermaid → Cytoscape consolidation
- Historical snapshots / diff (depends on the v1.0 Tier 2 backend
  which is now in place; can land as a v1.x patch)
- Dark mode

## What's next — v1.0.x patches

Small, additive improvements that don't need a minor bump:

- Pagination / response-shape trimming on `cluster-view` and
  `namespace-view` to bring 5K+ aggregator latency under 1 s.
- `CycleReport.category` field (`bootstrap_cert` | `dependency`
  | `structural`) so dashboards collapse benign cert cycles
  without a name-pattern filter.
- Tier 1 perf-baseline capture (Tier 2 baseline shipped in v1.0).

## v1.1 themes (active)

The next minor cut, sketched broadly:

- **Multi-cluster federation** — one KubeAtlas instance, N
  clusters. Likely "edge per cluster sending deltas to a hub"
  model.
- **cosign-verified rule pack loading** — the OCI loader
  already reserves a `--verify-signature` flag; v1.1 wires the
  cosign verification path so rule packs can be required-signed
  at install time.
- **Headlamp plugin** — same data, embedded in the Headlamp
  shell rather than the standalone UI.
- **Frontend Mermaid → Cytoscape consolidation** — the Phase 1
  Mermaid neighbour view replaced by a unified Cytoscape
  rendering. Bundle size drops, UX is consistent.
- **Historical snapshots / diff** — "what changed in the last
  hour?" Backed by the v1.0 Tier 2 backend.
- **Dark mode.**

## Phase 3 → v2.0+ (sketch)

Direction, not commitment:

- **Third-party Kubernetes platform integration** — first-class support for the distros teams actually run, going beyond "it talks to a kubeconfig":
  - **Amazon EKS** — read the `aws-auth` ConfigMap, surface IRSA bindings (ServiceAccount → IAM role), recognise EKS-Anywhere quirks.
  - **Azure AKS** — read managed-identity bindings (ServiceAccount → AAD identity), surface AKS-specific add-ons.
  - **Google GKE** — read Workload Identity bindings (ServiceAccount → GCP service account), recognise Autopilot resource constraints.
  - **Red Hat OpenShift** — model OpenShift `Route` as a first-class edge alongside `Ingress`/`HTTPRoute`, recognise `DeploymentConfig` and `BuildConfig`, ship installation docs that work with OpenShift's stricter SCC defaults.
  Verified install paths, platform-specific Helm values examples, and an integration-test matrix per platform.
- **Cloud-resource integration** — surface AWS/GCP/Azure objects (S3 buckets, IAM roles, Cloud SQL instances) that K8s resources reference, so the graph spans the cluster boundary. Builds naturally on top of the platform integration above (the IAM/identity bindings give us the edges to follow off-cluster).

## Compatibility promises

- **From v0.1.0 onward**: semver. A field added to `graph.Resource` or `graph.Edge` is a minor-version event; renaming or removing one is a major-version event.
- **`-once` CLI mode** stays available through the v0.x line as a scriptable scrape path.
- **Helm values schema** changes additively where possible; breaking renames are called out in CHANGELOG and accompany a migration note.

## How to influence the roadmap

- **Open an issue** on [GitHub](https://github.com/lithastra/kubeatlas/issues) describing the use case (not the proposed solution).
- **Reactions on existing issues** are read as priority signal.
- **PRs welcome** for items already on the roadmap; for items not on it, open an issue first so we can talk shape before you spend time.
- For v1.1 priorities specifically, the order will partly reflect what v1.0 users ask for first. If your team would adopt v1.x conditional on a particular feature, say so in an issue.
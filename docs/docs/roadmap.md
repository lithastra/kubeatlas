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

**v0.1.0 is released** (2026-05-06). Install with
`helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas --version 0.1.0`
— see the [Quick Start](./quick-start.md). Phase 2 is now the active
cycle.

| Phase | Status | What it delivered |
|---|---|---|
| **Phase 0** (Foundation) | ✅ Done | CLI binary, in-memory graph, 8 edge types, 16 watched resources, contract-tested store interface, contributor docs, CI gates. No API, no UI, no Helm Chart. |
| **Phase 1** (MVP → v0.1.0) | ✅ Released | REST + WebSocket API, React/MUI Web UI with Cytoscape topology and Mermaid neighbour view, Helm Chart with secure defaults, Playwright E2E, multi-platform release. Available as `oci://ghcr.io/lithastra/charts/kubeatlas:0.1.0`. |
| **Phase 2** (v1.0) | 🚧 Starting | Persistence, extensibility, advanced graph kinds. Priorities being shaped by v0.1.0 user feedback — see [How to influence the roadmap](#how-to-influence-the-roadmap). |
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
   v0.1.0 deploys as a single Pod with no external dependencies —
   no search backend, no message queue, no sidecars. Onboarding
   is `helm install` plus a port-forward. Extending the edge
   schema is one Go file plus one test (see
   [Adding a new edge type](./developer-guide.md#adding-a-new-edge-type--a-worked-example));
   v1.0 extends this to runtime Rego/Wasm extractors so operators
   can declare custom edges without rebuilding. The codebase is
   small enough that a contributor can read the full graph engine
   in an afternoon.

Karpor and KubeAtlas can coexist on the same cluster — they answer
different questions. If you need cross-cluster search, indexing,
and a centralized insight pipeline, Karpor is the right shape for
that. If you need to understand a single cluster's structure
right now with a tool you can install in five minutes and remove
just as easily, KubeAtlas is the right shape for that.

### Other tools you might evaluate alongside

A short list, with the question each is best at:

- **[Headlamp](https://headlamp.dev/) / [Lens](https://k8slens.dev/)** — "Show me everything in this cluster, navigably." General-purpose K8s UIs. KubeAtlas v0.1.0 ships its own UI for the graph; v1.0+ also targets a Headlamp plugin.
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

So we ship something usable instead of trying to ship everything:

- ❌ Built-in authentication — operators provide it via the Ingress layer (oauth2-proxy / Pomerium / Cloudflare Access; example values shipped)
- ❌ Persistence — Tier 1 in-memory only; restart loses graph state
- ❌ Multi-cluster — one kubeconfig per KubeAtlas instance
- ❌ Custom edge types via Rego/Wasm — the eight built-in edges are it for v0.1.0
- ❌ Dynamic CRD discovery — the 16 watched GVRs are hard-coded
- ❌ RBAC graph and NetworkPolicy graph
- ❌ Historical snapshots / diff
- ❌ Dark mode
- ❌ Headlamp plugin

These are **planned for v1.0**, not abandoned.

## Phase 2 → v1.0 (active)

The "make it suitable for production observability" cycle. Roughly
three months of work, broadly:

| Theme | What lands |
|---|---|
| **Persistence** | **Tier 2 storage**: PostgreSQL ≥ 14 with the [Apache AGE](https://age.apache.org/) extension. The `GraphStore` interface is already designed for this — Phase 0's contract test suite lets a Postgres backend drop in without touching callers. Multi-replica deployments become possible. |
| **Extensibility** | **Rego/Wasm extractors**: declare custom edge types in Rego policy, no fork required. **Dynamic CRD discovery**: KubeAtlas walks the cluster's CRD list at startup and watches anything that opts in via annotation. |
| **More edge kinds** | RBAC graph (Role/ClusterRole → Subject), NetworkPolicy reachability, ingress-class-aware route resolution. |
| **Time** | Historical snapshots written to PG; diff between two timestamps via the API and UI ("what changed in the last hour?"). |
| **Impact radius** | "If I delete this Secret, here's the transitive blast radius" — a multi-hop traversal exposed as both an API and a UI panel. |
| **Headlamp plugin** | Separate repository (`lithastra/kubeatlas-headlamp-plugin`) — same data, embedded in the Headlamp shell rather than the standalone UI. |
| **TLS** | cert-manager integration in the Helm Chart for Ingress (today's v0.1.0 examples use static `secretName`). |
| **Performance** | L4 regression suite running weekly; >20% latency regression pages on call. |

## Phase 3 → v2.0+ (sketch)

Direction, not commitment:

- **Multi-cluster federation** — one KubeAtlas instance, many clusters, unified graph. Likely model: an "edge" KubeAtlas per cluster sending deltas to a "hub".
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
- For Phase 2 priorities specifically, the order will partly reflect what v0.1.0 users ask for first. If your team would adopt v0.1.0 conditional on a particular Phase 2 feature, say so in an issue.
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

**Phase 4 has begun.** Phase 3 shipped three releases — v1.1 (rule
packs and plugins), v1.2 (offline rendering), and v1.3 (multi-cluster
federation, platform-identity edges, cartography UI). **v1.4** opens
Phase 4 with offline diagnostics (F-301), Gatekeeper/Kyverno policy
visibility (F-205), opt-in anonymous telemetry, and v1alpha1 usage
tracking ahead of the eventual v2.0 removal. Install with
`helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas --version 1.4.0`
— see the [Quick Start](./quick-start.md).

| Phase | Status | What it delivered |
|---|---|---|
| **Phase 0** (Foundation) | ✅ Done | CLI binary, in-memory graph, 8 edge types, 16 watched resources, contract-tested store interface, contributor docs, CI gates. No API, no UI, no Helm Chart. |
| **Phase 1** (MVP → v0.1.0) | ✅ Released | REST + WebSocket API, React/MUI Web UI with Cytoscape topology and Mermaid neighbour view, Helm Chart with secure defaults, Playwright E2E, multi-platform release. Available as `oci://ghcr.io/lithastra/charts/kubeatlas:0.1.0`. |
| **Phase 2** (→ v1.0) | ✅ Released | Tier 2 persistence (PostgreSQL + Apache AGE), Rego rule packs, RBAC graph, blast radius, orphan + cycle detection, `/api/v1/*` GA, cert-manager TLS, OpenShift detector + embedded pack, chaos test suite. Available as `oci://ghcr.io/lithastra/charts/kubeatlas:1.0.0`. |
| **Phase 3** (→ v1.1 / v1.2 / v1.3) | ✅ Released | Cloud rule packs, snapshots, search, plugins (v1.1). Offline `kubectl atlas`, graph-image export (v1.2). Multi-cluster federation, platform-identity edges, HPA support, GitHub Action, cartography Web UI redesign (v1.3). |
| **Phase 4** (→ v1.4 / v1.5 / v2.0) | 🚧 In progress | Offline diagnostic report (F-301), Gatekeeper/Kyverno policy visibility (F-205), opt-in anonymous telemetry, v1alpha1 usage counters, Backstage plugin (v1.4 — released). OTel runtime overlay (F-204) and credential rotation (v1.5), GraphStore v2 and the v1alpha1 removal (v2.0) planned. |
| **Beyond Phase 4** | 💭 Sketch | Cloud-resource integration, third-party platform deep-dives, federation cross-cluster edge inference. |

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

- **[Headlamp](https://headlamp.dev/) / [Lens](https://k8slens.dev/)** — "Show me everything in this cluster, navigably." General-purpose K8s UIs. KubeAtlas ships its own UI for the graph; a Headlamp plugin (shipped in v1.1) lives in `lithastra/kubeatlas-headlamp-plugin`.
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

The first seven shipped in v1.0; the Headlamp plugin shipped in
v1.1.

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

### Shipped in v1.1

These were in the original Phase 2 plan but moved out for the
v1.0 cut and landed in v1.1:

- Headlamp plugin (`lithastra/kubeatlas-headlamp-plugin`)
- Historical snapshots / diff

## Phase 3 → v1.1 / v1.2 / v1.3 (in progress)

Phase 3 widens KubeAtlas beyond a single cluster's core resources
and reaches it from places besides the in-cluster UI. It ships in
three releases.

### v1.1 (released) — rule packs, snapshots, search, plugins

Shipped scope:

| Theme | What landed |
|---|---|
| **Cloud-platform rule packs** | Opt-in EKS / AKS / GKE add-on CRD packs in the sibling `lithastra/kubeatlas-rules` repo — AWS Load Balancer Controller, Karpenter, GKE Ingress, Multi-cluster Services, and more. Loaded via `rulePacks.extras`. |
| **Historical snapshots** | An append-only resource-change event stream with `GET /api/v1/snapshots/diff` — "what changed in the last hour?" Tier 2; configured under `snapshots.*`. |
| **Full-text search** | Ranked `GET /api/v1/search` over resource name, kind, namespace, and label values; indexed on Tier 2. |
| **Label filtering** | `label.<key>=<value>` narrowing on the cluster / namespace views, plus a `GET /api/v1/labels` vocabulary endpoint. |
| **NetworkPolicy edges** | `NetworkPolicy` is first-class — the Pods a policy selects and the peers it allows. |
| **Rule-pack signing** | Keyless Sigstore signature verification for OCI rule packs (`rulePacks.verifySignature`). |
| **Plugins** | The `kubectl atlas` plugin and a [Headlamp](https://headlamp.dev) plugin (separate repo). |

### v1.2 (released) — offline rendering and a self-contained plugin

KubeAtlas usable without a server in the cluster. Shipped scope:

- **Offline `kubectl atlas`** — the plugin builds the dependency
  graph straight from the Kubernetes API and renders it locally:
  a static SVG by default, or an interactive in-process web UI
  with `--local-ui`. The plugin is self-contained — it no longer
  needs a separate `kubeatlas` binary.
- **Graph-image export** — `kubeatlas -once -format=svg` and a
  `GET /api/v1/export` endpoint render cluster / namespace views
  as SVG or PNG.
- **Cluster selection** — the CLI and the plugin honour the
  standard `--context` / `--kubeconfig` flags.
- **Rule-pack signature verification on by default** —
  `rulePacks.verifySignature` defaults to `true`; air-gapped
  installs must set it `false` explicitly.

### v1.3 (released) — multi-cluster, platform identity, cartography UI

The final Phase 3 release. Stretching the graph across cluster
boundaries and replacing the Headlamp-styled web shell with a
purpose-built cartography UI:

- **Multi-cluster federation (F-201)** — one KubeAtlas instance, N
  clusters. A new `pkg/multicluster/` package, a `ClusterID` on the
  graph model, federation aggregator and `/federation` route group,
  and cluster-scoped WebSocket subscriptions.
  `KUBEATLAS_MULTICLUSTER_KUBECONFIG_DIR` points at a directory of
  per-cluster kubeconfigs (one file per cluster, filename = cluster
  ID). `GET /api/v1/federation/{clusters,graph}` is the read surface;
  the Web UI **LeftClusterStrip** is wired to it for cluster picking.
- **Platform-identity edges (F-209)** — `BINDS_PLATFORM_IDENTITY`
  from a ServiceAccount to a synthetic `ExternalIdentity` representing
  the cloud account it is bound to:
  - **EKS** — `eks.amazonaws.com/role-arn` annotation.
  - **AKS** — `azure.workload.identity/client-id` label.
  - **GKE** — `iam.gke.io/gcp-service-account` annotation.
- **HorizontalPodAutoscaler support** — new `SCALES` edge type from
  an HPA to whatever its `spec.scaleTargetRef` names (Deployment /
  StatefulSet / any /scale-bearing kind).
- **`kubeatlas-action`** — a new repo `lithastra/kubeatlas-action`
  so KubeAtlas can run in GitHub Actions CI pipelines, rendering the
  dependency graph as an SVG artifact.
- **Cartography Web UI redesign** — the whole web shell rebuilt
  around the "one graph, many modes" design:
  - 5 runtime-switchable themes (Parchment / Survey / Terrain /
    Ink / Slate) sharing one CSS-variable contract.
  - Persistent time axis with diff-mode anchor presets (1h / 4h /
    24h / 7d) that highlight added / removed / modified resources
    on the canvas.
  - Blast-radius mode (BFS from selected node with depth +
    direction controls, canvas dim/brighten, hop-by-hop summary).
  - ⌘K command palette over `/api/v1alpha1/search` with matched
    nodes highlighted on the canvas.
  - Zoom-scale widget mapping cytoscape zoom × → L1–L4 bands.
  - Edge-type filter presets (All / RBAC / Network / Config /
    Storage) that fold a sub-graph into the canvas without route
    changes.
  - Left cluster strip wired to the federation cluster list with
    deterministic per-cluster chip colours.
- **v1.3 perf baseline** — dual-tier (Tier 1 + Tier 2) on the
  10K-resource stress fixture; multi-cluster merge bench and a
  cluster-disconnect chaos scenario.

### v1.3.1 (shipped)

Polish items deferred from v1.3.0, now shipped in v1.3.1:

- **Cluster picker → federation graph wiring** — the
  LeftClusterStrip routes the selected cluster through
  `/api/v1/federation/graph?cluster=…` with per-cluster border
  tints via a deterministic hash palette.
- **Drag-anchor on the time-axis rail** — the rail is a real ARIA
  slider with drag + keyboard support and a 30s right-edge snap.
- **Keyboard graph traversal** — Arrow keys walk the node list,
  Enter/Space opens the detail panel, Esc clears.
- **Screen-reader announcements** — polite `aria-live` region on
  blast-radius enter, diff anchor change, cluster focus, and
  command-palette open.
- **Slate theme WCAG AA contrast fix** — `text-3` bumped from
  `#6B7079` to `#888E98`.
- **RadialMenu + right-click depth picker** — enter blast-radius
  mode at depth 1 / 3 / ∞ in a single gesture.
- **`kubectl atlas --version`** — stamps version + commit + date.
- **goreleaser `dockers_v2`** — single buildx-native block.
- **RELEASING.md generalised** — steady-state recipe for all
  future releases.

### v1.3.x remaining follow-ups

- **FLIP zoom transitions** — the zoom-scale widget animates the
  cytoscape zoom level today; the aggregated → expanded node
  split/merge with the design's 400ms FLIP choreography is queued.

## Phase 4 → v1.4 / v1.5 / v2.0 (in progress)

### v1.4 (shipped) — offline diagnostics, policy visibility, telemetry

- **Offline diagnostic report (F-301)** — `kubeatlas diagnose` and
  `GET /api/v1/diagnose` produce a self-contained HTML/JSON snapshot
  (graph, orphans, cycles, top blast radius) from an offline scan,
  for air-gapped audits and CI. The JSON carries a normalized
  `policyViolations` array.
- **Policy visibility (F-205)** — Gatekeeper Constraints and Kyverno
  policies surface as `ENFORCES` edges (with violation status) via a
  dynamic informer-of-informers that discovers Constraint CRDs at
  runtime. New `/api/v1/policy/constraints` + `/affected` endpoints
  and a Web UI Policy view.
- **Opt-in anonymous telemetry** — off by default; when enabled,
  sends coarse non-identifying usage once a day to a hard-coded
  endpoint, with a transparent `/api/v1/telemetry/preview` and a
  documented [trust contract](./concepts/telemetry-schema.md).
- **v1alpha1 usage counters** — `kubeatlas_api_v1alpha1_requests_total`
  vs `kubeatlas_api_v1_requests_total`, the data behind the v2.0
  removal decision.
- **Ecosystem** — Backstage plugin (v0.1), Headlamp Policy view, and
  a GitHub Action `policy-report` option.

### Phase 4 remaining (planned)

- **v1.5** — OpenTelemetry runtime overlay (F-204: observed
  `CALLS_AT_RUNTIME` edges layered over the declarative graph),
  multi-cluster credential rotation, and a `v1alpha1` sunset notice
  in responses.
- **v2.0** — a `GraphStore` v2 interface, and removal of the
  `v1alpha1` API once the usage counters justify it (a tracked,
  announced deprecation — not a surprise break).

## Beyond Phase 4 (sketch)

Direction, not commitment:

- **Third-party Kubernetes platform deep-dives** — going beyond
  the identity edges v1.3 lands:
  - **Amazon EKS** — recognise EKS-Anywhere quirks; deeper Karpenter / AWS LBC modeling.
  - **Azure AKS** — surface AKS-specific add-ons beyond Workload Identity.
  - **Google GKE** — recognise Autopilot resource constraints.
  - **Red Hat OpenShift** — model `Route` as a first-class edge alongside `Ingress`/`HTTPRoute`, recognise `DeploymentConfig` and `BuildConfig` natively, ship installation docs that work with OpenShift's stricter SCC defaults.
  Verified install paths, platform-specific Helm values examples, and an integration-test matrix per platform.
- **Cloud-resource integration** — surface AWS/GCP/Azure objects (S3 buckets, IAM roles, Cloud SQL instances) that K8s resources reference, so the graph spans the cluster boundary. Builds on the platform-identity edges from v1.3 (the IAM/identity bindings give us the edges to follow off-cluster).

## Compatibility promises

- **From v0.1.0 onward**: semver. A field added to `graph.Resource` or `graph.Edge` is a minor-version event; renaming or removing one is a major-version event.
- **`-once` CLI mode** stays available across the v1.x line as a scriptable scrape path.
- **Helm values schema** changes additively where possible; breaking renames are called out in CHANGELOG and accompany a migration note.

## How to influence the roadmap

- **Open an issue** on [GitHub](https://github.com/lithastra/kubeatlas/issues) describing the use case (not the proposed solution).
- **Reactions on existing issues** are read as priority signal.
- **PRs welcome** for items already on the roadmap; for items not on it, open an issue first so we can talk shape before you spend time.
- v1.3 scope is set (multi-cluster + platform identity + Action), but the order **within** v1.3 — and what lands first beyond Phase 3 — will partly reflect what current users ask for first. If your team would adopt a future release conditional on a particular feature, say so in an issue.
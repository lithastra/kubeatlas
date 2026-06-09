# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
KubeAtlas uses [Semantic Versioning](https://semver.org/) — breaking
changes bump the major number, additive changes bump the minor,
fixes bump the patch.

## [Unreleased]

_(none yet)_

## [v1.4.0] — offline diagnostics, policy visibility, opt-in telemetry

v1.4.0 adds a self-contained offline diagnostic report for
air-gapped audits and CI, makes admission-policy enforcement a
first-class part of the graph (Gatekeeper Constraints and Kyverno
policies surface as `ENFORCES` edges via runtime CRD discovery),
ships the project's first opt-in anonymous
usage telemetry (off by default, with a documented trust contract),
and begins tracking `v1alpha1` vs `v1` API usage to inform the v2.0
removal. The `v1alpha1` and `/api/v1/*` surfaces are unchanged;
everything below is additive.

### Added

- **Offline diagnostic report** — `kubeatlas diagnose` and
  `GET /api/v1/diagnose` produce a self-contained snapshot (the full
  dependency graph, orphans, cycles, and the top blast-radius
  resources) as HTML (air-gapped — no external CDN references) or
  JSON. The report runs an offline scan against the current
  KUBECONFIG and needs no running server. Its JSON carries a
  normalized `policyViolations` array distilled from the graph's
  ENFORCES edges, so automation has one engine-agnostic contract.
- **Policy visibility** — Gatekeeper Constraints and Kyverno
  ClusterPolicies/Policies are watched and rendered as `ENFORCES`
  edges from each policy to the resources it governs, tagged with
  current violation status. Discovery is dynamic: an
  informer-of-informers watches `ConstraintTemplate`s and registers
  an informer for each generated Constraint CRD at runtime, so
  policies that appear after startup are picked up without a
  restart. New engine-aware endpoints:
  - `GET /api/v1/policy/constraints` (optional `?engine=gatekeeper`
    or `?engine=kyverno`) — the constraints/policies in force.
  - `GET /api/v1/policy/constraints/{name}/affected` — the resources
    a constraint governs, with per-resource violation state.
  A **Policy view** in the Web UI lists them and styles `ENFORCES`
  edges distinctly on the canvas.
- **`ENFORCES` edge type + edge attributes** — the graph model gains
  a 16th edge type and an optional per-edge `Attributes` map
  (carrying violation status); the Postgres store adds an
  `attributes` JSONB column (schema migration 008).
- **Opt-in anonymous telemetry** — off by default
  (`telemetry.enabled=false`). When enabled, KubeAtlas sends coarse,
  non-identifying data once a day (version, K8s version, OS/arch,
  tier, a resource-count bucket, enabled rule-pack names, cluster
  count). It never sends resource names, namespaces, label values,
  IPs, or any cross-session identifier, and the receiver endpoint is
  hard-coded (not configurable). `GET /api/v1/telemetry/status` and
  `GET /api/v1/telemetry/preview` make the exact payload inspectable.
  The schema is documented field-by-field in
  `docs/concepts/telemetry-schema.md`; a chaos test covers an
  unreachable endpoint and a CI job asserts zero egress while
  disabled.
- **Per-version API usage counters** —
  `kubeatlas_api_v1alpha1_requests_total` and
  `kubeatlas_api_v1_requests_total` track the deprecated vs current
  surface, the data behind the eventual v2.0 removal decision.
- **Ecosystem** — a Backstage frontend plugin (v0.1), a Headlamp
  Policy view, and a `policy-report` option for the GitHub Action
  (appends a policy-violation summary to PR comments) ship alongside
  this release from their own repositories.

### Changed

- **`graph.Edge` carries an optional `Attributes` map** across the
  model, the memory store, and the Postgres store, so edge types can
  attach typed metadata — first used by `ENFORCES` for violation
  status.

## [v1.3.1] — federation UI wiring, accessibility, tooling polish

v1.3.1 is the first patch after the v1.3 release. It ships the
federation cluster picker → graph wiring that was deferred from
v1.3.0, a full accessibility pass (keyboard traversal, screen-reader
announcements, contrast fix), and several tooling improvements.

### Added

- **Cluster picker drives the topology fetch through federation
  graph** — picking a cluster from the LeftClusterStrip now routes
  the canvas query through `/api/v1/federation/graph?cluster=<id>`
  (previously always `/api/v1alpha1/graph` regardless of
  selection). Each node picks up a per-cluster border tint via a
  shared deterministic hash so federated members are visually
  distinct on the canvas without an extra legend.
- **Keyboard graph traversal on the topology canvas** —
  `role="application"` + `tabIndex=0` so the cytoscape canvas lands
  in the tab order; Arrow keys walk the id-sorted node list,
  Enter/Space opens the focused node in the right detail panel,
  Esc clears (deferring to the shell-level Esc handler when blast-
  radius or diff mode owns it).
- **Drag-anchor scrubbing on the time-axis rail** — the rail is
  now a real ARIA slider with drag + Arrow/Home/End/Shift-Arrow
  keyboard support and a 30s right-edge snap to NOW. Picks an
  arbitrary anchor within the last 7 days; preset chips still work
  and the marker position is the real fraction of the rail span.
- **RadialMenu primitive + right-click depth picker** — generic
  wedge menu in `web/src/design/`, used as the right-click entry
  path on the topology canvas to enter blast-radius mode at one of
  three common depths (1 / 3 / ∞) in a single gesture. The linear
  toolbar at canvas bottom stays as the always-visible alternative.
- **Screen-reader announcements on mode change** — polite
  `aria-live` region at the shell speaks on blast-radius enter,
  diff anchor change, cluster focus, and command-palette open. A
  zero-width-space toggler forces repeated identical messages to
  re-fire (some SRs ignore unchanged textContent).
- **`kubectl atlas --version` flag** — stamps `pkg/version`'s
  Version + Commit + Date into the kubectl-atlas binary at release
  time so installs can self-identify without poking around
  `~/.krew/store`.

### Changed

- **GitHub release bodies auto-populate from `CHANGELOG.md`** —
  goreleaser now reads `/tmp/release-notes.md` (produced by a new
  `make changelog-extract VERSION=vX.Y.Z` target) via
  `--release-notes`. The previous static Phase-1-style header made
  every release card look identical.
- **`RELEASING.md` generalised** — replaces the one-time
  `RELEASING-v1.3.0.md` with the steady-state recipe used for every
  future release. Folds in the changelog-extract flow, the
  kubectl-atlas --version sanity check, and the helm-pull 403 /
  token-endpoint gotcha surfaced during the v1.3.0 cut.
- **goreleaser `dockers` + `docker_manifests` → `dockers_v2`** —
  one buildx-native block replaces the per-arch builds plus manual
  manifest stitching; `goreleaser check` is now warning-free.
- **krew plugin manifest bumped to v1.3.0** — six platforms, SHAs
  verified locally via `kubectl krew install --manifest`.

### Fixed

- **Slate theme `text-3` fails WCAG AA contrast** — bumped from
  `#6B7079` to `#888E98` (3.39:1 → 5.11:1 on `bg`,
  3.00:1 → 4.53:1 on `surface`). Both sources stay in sync
  (`themePalettes.ts` and `atlas-themes.css`). `text-1` and
  `text-2` already cleared AA on every Slate surface; `border`
  fails 3:1 but is a decorative divider (WCAG 1.4.11 exempts).

## [v1.3.0] — multi-cluster federation, platform identity, cartography UI

v1.3.0 is the third release in the v1.1/v1.2/v1.3 line. It stretches the
dependency graph across cluster boundaries (one KubeAtlas instance
attaching to N kubeconfigs, every resource tagged with its origin,
a federated read surface), promotes the cloud-identity bindings
that until now lived as opaque annotations (EKS IRSA, AKS Workload
Identity, GKE Workload Identity) into first-class graph edges, adds
HorizontalPodAutoscaler as a watched kind with a new `SCALES` edge,
and rebuilds the Web UI around a cartography design — one full-bleed
graph canvas, five runtime-switchable themes, a ⌘K command palette,
a blast-radius mode, a persistent time-axis diff, and an edge-type
filter, all folding into the same canvas instead of replacing it.
The `v1alpha1` and `/api/v1/*` surfaces are unchanged beyond the new
federation paths; everything below is additive.

### Added

- **Multi-cluster federation (data layer)** — opt-in
  (`multicluster.enabled=true` + `multicluster.kubeconfigSecret`),
  a single KubeAtlas pod attaches to every kubeconfig the named
  Secret carries. Each member runs its own informer pipeline against
  a shared graph store; every resource is tagged with a `ClusterID`
  so the federation aggregator can answer cluster-scoped queries
  without re-architecting the store. The new endpoints are:
  - `GET /api/v1/federation/clusters` — `{mode, clusters}`; the UI
    uses `mode` to detect whether federation is on.
  - `GET /api/v1/federation/graph?cluster=a,b` — a flat
    `FederatedView` (resources + intra-cluster edges) across the
    named members. Unknown cluster names return 400 so a stale
    bookmark doesn't silently render an empty selection.
  Cross-cluster edge inference is deliberately not done in v1.3;
  the design says explicit-only (planned via an
  `kubeatlas.io/external-ref` annotation in a future release). The
  Web UI's **left cluster strip** is wired to
  `/api/v1/federation/clusters` for member selection with
  deterministic per-cluster chip colours; routing the picked
  cluster through `/federation/graph` for the topology fetch is a
  v1.3.x polish item (the picker state is live today; the canvas
  still uses `/api/v1alpha1/graph`).
- **Platform-identity edges** — three new built-in extractors
  emit `BINDS_PLATFORM_IDENTITY` edges from a ServiceAccount to a
  synthetic `ExternalIdentity` endpoint that represents the cloud
  identity it is bound to:
  - **EKS IRSA** — `eks.amazonaws.com/role-arn` annotation.
  - **AKS Workload Identity** — `azure.workload.identity/client-id`
    label.
  - **GKE Workload Identity** — `iam.gke.io/gcp-service-account`
    annotation.
  KubeAtlas calls no cloud SDK to validate any of these — the edge
  is derived purely from the K8s metadata. In multi-cluster mode
  the synthetic endpoint id carries the cluster prefix so two
  clusters referencing the same external identity stay distinct
  nodes.
- **OpenShift `Route` as a built-in `ROUTES_TO` source** — Route
  (route.openshift.io/v1) joins Ingress and HTTPRoute as a built-in
  edge instead of relying on the OpenShift rule pack. The rule pack
  stays for the extra-depth rules (DC trigger chains, weighted
  backends) it was always carrying.
- **Snapshots Web UI page** — a new top-level "Snapshots" page
  surfaces the Tier 2 snapshot endpoints: a window-picker driven
  diff with three tables (Added / Modified / Removed) plus a
  timeline of recent full-sync markers. A Tier 1 install (or one
  with snapshots disabled) sees a clear "snapshots not enabled"
  banner rather than a generic error.
- **HorizontalPodAutoscaler graph support** — `autoscaling/v2`
  HPAs are watched by a new built-in `ScalesExtractor` that emits
  a `SCALES` edge from each HPA to whatever its
  `spec.scaleTargetRef` names (Deployment, StatefulSet, or any
  `/scale`-bearing kind). Multi-cluster aware — the target ID
  carries the source HPA's `ClusterID`.
- **Cartography Web UI redesign** — the whole web shell rebuilt
  around the "one graph, many modes" design. A single full-bleed
  Cytoscape canvas under a persistent shell replaces the
  per-route MUI pages:
  - **Five runtime-switchable themes** (Parchment / Survey /
    Terrain / Ink / Slate) sharing one CSS-variable contract,
    plus the corresponding Cytoscape palette swap that preserves
    selection on theme change.
  - **⌘K command palette** over `/api/v1alpha1/search` with
    matched node IDs highlighted on the canvas — matches stay
    legible after the overlay closes (the IA's
    search-folds-into-graph principle).
  - **Blast-radius mode** — BFS from the selected node with
    depth (1 / 2 / 3 / 5 / ∞) and direction (↓ / ↑ / ↕) controls,
    canvas dim/brighten, top banner with affected count, right-
    panel hop-by-hop summary. Esc to exit.
  - **Time-axis diff** — anchor preset chips (1h / 4h / 24h /
    7d) on the persistent time bar enter diff mode; canvas
    decorates added (green halo) / removed (dashed muted) /
    modified (amber overlay) nodes from
    `/api/v1/snapshots/diff`; right-panel change log.
  - **Edge-type filter chip** — All / RBAC / Network / Config /
    Storage presets fold a sub-graph into the canvas (dims
    edges + orphaned nodes) without a route change.
  - **Zoom-scale widget** — bottom-right chip maps the
    cytoscape zoom × to one of four L-bands and offers
    400 ms animated jumps between them.
  - **Cartography signature** — north-up compass at the
    canvas bottom-left, latitude/longitude grid background
    via CSS gradients (zero cytoscape cost).
  - **A11y starter pass** — skip-to-graph link in the top bar,
    `aria-live` on the right panel, focus-visible rings on every
    interactive surface, `prefers-reduced-motion` honoured.
  - **Resources page** — Search chip beside the title opens the
    same ⌘K palette; table columns autosize to content.

### Changed

- **Multi-cluster ID propagation** — every built-in extractor that
  synthesises target IDs (`owns`, `configmap`, `secret`, `volume`,
  `attached`, `routes`, `serviceaccount`, `rbac`, plus the new
  platform-identity ones) now propagates `ClusterID` into the
  target id. Selector extractors (`Service`, `NetworkPolicy`) scope
  their store queries by `ClusterID`. Single-cluster installs are
  unchanged: empty `ClusterID` reproduces the v1.2 baseline exactly,
  including the literal id format. The change matters only when
  `multicluster.enabled=true`; without it a federated install would
  have produced dangling edges across the cluster boundary.

[v1.4.0]: https://github.com/lithastra/kubeatlas/releases/tag/v1.4.0
[v1.3.1]: https://github.com/lithastra/kubeatlas/releases/tag/v1.3.1
[v1.3.0]: https://github.com/lithastra/kubeatlas/releases/tag/v1.3.0

## [v1.2.0] — offline rendering and a self-contained kubectl plugin

v1.2.0 is the second of the v1.1/v1.2/v1.3 release points. It makes
KubeAtlas useful without a server running in the cluster: the
`kubectl` plugin now builds and renders the dependency graph itself
— straight from the Kubernetes API on the operator's machine — and
the CLI and API gained graph-image export. The `v1alpha1` and
`/api/v1/*` surfaces are unchanged; everything below is additive.

### Added

- **Offline `kubectl atlas`** — the `kubectl-atlas` plugin works
  without a KubeAtlas server in the cluster. By default it builds
  the dependency graph directly from the Kubernetes API and renders
  a static SVG with the graphviz `dot` tool. `--local-ui` instead
  runs a KubeAtlas server in-process and opens the interactive web
  UI — no graphviz and no in-cluster server — with `--host` to
  choose the bind address. `--online` (or `--server` /
  `KUBEATLAS_URL`) keeps the original behaviour of opening a live
  in-cluster UI. The plugin is self-contained: it no longer shells
  out to a separate `kubeatlas` binary. `--context` / `--kubeconfig`
  select the cluster, `--no-browser` prints the URL instead of
  opening one, and the client-go auth plugins are registered so it
  can reach OIDC-authenticated clusters.
- **`kubeatlas -once -format`** — the one-shot CLI mode can now emit
  `json` (default), `dot`, or `svg`, so `kubeatlas -once -format=svg`
  renders a graph image without a separate `dot` pipeline.
- **Graph-image export endpoint** — `GET /api/v1/export` (and
  `/api/v1alpha1/export`) renders a cluster or namespace view as an
  `svg` or `png` image server-side. See
  [decision 0012](https://github.com/lithastra/kubeatlas/blob/main/docs/decisions/0012-server-side-render.md).
- **`-context` / `-kubeconfig` flags** — the `kubeatlas` CLI honours
  the standard kubectl cluster-selection flags for local runs
  instead of always using the kubeconfig's current context.

### Changed

- The `kubectl-atlas` plugin now defaults to **offline** rendering.
  In v1.1.0 it only opened a live in-cluster UI; that behaviour now
  requires `--online` (or `--server` / `KUBEATLAS_URL`).
- **Rule-pack signature verification is on by default.**
  `rulePacks.verifySignature` now defaults to `true` — it shipped
  `false` in v1.1 as a one-release migration window. An install
  that loads unsigned OCI rule packs must sign them or set
  `rulePacks.verifySignature: false` explicitly, the only supported
  mode for an air-gapped cluster with no path to the Sigstore trust
  root. First-party `ghcr.io/lithastra` packs are signed and
  unaffected.

[v1.2.0]: https://github.com/lithastra/kubeatlas/releases/tag/v1.2.0

## [v1.1.0] — cloud rule packs, snapshots, search, plugins

v1.1.0 is the first of the v1.1/v1.2/v1.3 release points. It widens
KubeAtlas beyond a single cluster's core resources: managed-Kubernetes
add-on rule packs, a queryable change history, full-text search, and
two new ways to reach the graph — a `kubectl` plugin and a Headlamp
plugin. The `v1alpha1` and `/api/v1/*` surfaces from v1.0 are
unchanged; everything below is additive.

### Added

- **Cloud-platform rule packs** — opt-in Rego packs that model the
  CRDs managed-Kubernetes add-ons inject into a cluster, in the
  sibling `lithastra/kubeatlas-rules` repository:
  - **EKS** — AWS Load Balancer Controller `TargetGroupBinding`,
    Karpenter `NodePool` → `EC2NodeClass`, EKS Pod Identity.
  - **AKS** — AAD Pod Identity (`AzureIdentityBinding`).
  - **GKE** — GKE Ingress (`BackendConfig` / `FrontendConfig`),
    Multi-cluster Services, Backup for GKE, and Fleet membership.

  The packs model the Kubernetes view of a cluster only — no cloud
  resources, no cloud SDKs. Load them with `rulePacks.extras`. See
  [installation/eks](https://docs.kubeatlas.lithastra.com/installation/eks).
- **NetworkPolicy in the graph** — `NetworkPolicy` objects are
  first-class. KubeAtlas derives the Pods a policy selects and the
  ingress sources / egress destinations it declares — the policy's
  declared topology, not what a CNI actually enforces. New
  endpoints `GET /api/v1/networkpolicy/{ns}/{name}/selected` and
  `.../allow-graph`.
- **Historical snapshots (Tier 2)** — KubeAtlas can now record every
  resource add / update / delete into an append-only event stream
  and answer "what changed?". `GET /api/v1/snapshots/diff?from=&to=`
  returns the resources added, removed, and modified across a time
  window; `GET /api/v1/snapshots` lists the periodic full-sync
  markers. Configured under `snapshots.*` in the chart (default
  off; requires Tier 2), with a tunable retention window and a
  periodic full-sync `CronJob`. New `kubeatlas snapshot trigger`
  CLI subcommand. See
  [concepts/snapshots](https://docs.kubeatlas.lithastra.com/concepts/snapshots).
- **Full-text search** — `GET /api/v1/search` is now a ranked
  full-text search over resource name, kind, namespace, and label
  values. On Tier 2 it runs as one indexed query; on Tier 1 it
  falls back to a linear scan and says so. Queries accept free-text
  terms plus `kind:` / `namespace:` filters.
- **Label filtering** — `GET /api/v1/graph` accepts
  `label.<key>=<value>` parameters to narrow the cluster and
  namespace views to resources carrying a label. The new
  `GET /api/v1/labels` endpoint lists every label key and its most
  common values, and the web UI gains a label-filter control on the
  topology page.
- **Rule-pack signature verification** — OCI rule packs can be
  verified with keyless Sigstore signatures before they load
  (`rulePacks.verifySignature`, off by default in v1.1). A pack
  whose signature does not verify aborts startup.
- **`kubectl` plugin** — `kubectl-atlas` opens the KubeAtlas UI at
  the page for a resource, namespace, or the whole cluster:
  `kubectl atlas deployment api -n petclinic`.
- **Headlamp plugin v0.1** — a [Headlamp](https://headlamp.dev)
  plugin (in a separate repository) that renders the KubeAtlas
  dependency graph inside the Headlamp UI, with a cluster topology
  view and a per-resource "Dependencies" section.

### Changed

- Cluster- and namespace-view aggregation now executes inside the
  storage backend instead of materialising the whole graph in the
  API process. This resolves an out-of-memory crash on large
  clusters and cuts peak memory for those requests roughly 20-fold.
- `GET /api/v1/cycles` now classifies each dependency cycle as a
  certificate-bootstrap loop, an intentional cycle, or an unknown
  one, so operators can tell expected loops from real problems.

[v1.1.0]: https://github.com/lithastra/kubeatlas/releases/tag/v1.1.0

## [v1.0.0] — general availability

The first GA release:
persistent state, programmable rule packs, RBAC graph, blast
radius, orphan / cycle analysis, and a frozen v1alpha1 surface
plus a GA `/api/v1/*` superset.

### Added

- **Tier 2 persistence** — PostgreSQL + Apache AGE backend.
  Opt-in via `persistence.enabled=true` in the chart; embedded
  CloudNativePG sub-chart available at
  `persistence.embedded.enabled=true` for single-cluster
  installs. Restart now preserves the graph; informer cold-
  start drops to ~4 seconds with the cache hot. See
  [installation/persistence](https://docs.kubeatlas.lithastra.com/installation/persistence).
- **Rego rule packs** — CRD-driven edge derivation, no rebuild
  required. Embedded OpenShift pack auto-loads when the
  detector sees `route.openshift.io`. Extra packs load via
  OCI ref (`rulePacks.extras: ["oci://ghcr.io/.../...:0.1.0"]`)
  or via `--rule-pack <dir>` on the CLI. Rule packs live in a
  sibling repo (`lithastra/kubeatlas-rules`) and are signed +
  semver-pinned. See
  [concepts/rego-rules](https://docs.kubeatlas.lithastra.com/concepts/rego-rules).
- **RBAC graph** — `Role`, `RoleBinding`, `ClusterRole`,
  `ClusterRoleBinding` are first-class resources in the graph
  with `BINDS_SUBJECT` and `BINDS_ROLE` edges. New endpoints:
  - `GET /api/v1/rbac/serviceaccount/{ns}/{name}/permissions`
  - `GET /api/v1/rbac/role/{ns}/{name}/subjects`
  - `GET /api/v1/rbac/clusterrole/{name}/subjects`
- **Blast radius** — `GET /api/v1/blast-radius/{ns}/{kind}/{name}`
  walks incoming edges to return everything that depends on the
  target resource. Direction-aware traversal primitive on the
  store interface so Tier 1 (BFS) and Tier 2 (AGE Cypher) share
  semantics. P95 < 500ms on 5K-resource clusters. See
  [concepts/blast-radius](https://docs.kubeatlas.lithastra.com/concepts/blast-radius).
- **Orphan + cycle detection** —
  `GET /api/v1/orphans?namespace=<ns>` and
  `GET /api/v1/cycles`. Cycle detection uses Tarjan's SCC; on
  5K vertices / 5K edges it finishes in ~80ms. See
  [concepts/orphan-cycle](https://docs.kubeatlas.lithastra.com/concepts/orphan-cycle).
- **`kubeatlas export` subcommand** — emits Graphviz DOT for
  the cluster (or a single namespace), covering the
  permanent-CLI-export use case. Pipe through `dot -Tsvg`,
  `dot -Tpng`, etc. See [CLI reference](https://docs.kubeatlas.lithastra.com/cli-reference).
- **`/api/v1/*` GA endpoints** — every `/api/v1alpha1/*` route
  is also served at `/api/v1/*` from the same handler. The
  `ResourceDetailResponseV1` shape adds `blastRadiusCount`,
  `isOrphan`, and `inCycle` enrichment fields the v0.1.0
  shape didn't carry. See
  [concepts/api-versioning](https://docs.kubeatlas.lithastra.com/concepts/api-versioning).
- **cert-manager Helm integration** — opt-in
  `ingress.certManager.enabled=true`. Three modes
  (`selfsigned`, `letsencrypt-staging|prod`, `custom`). The
  chart auto-injects the cert-manager-managed Secret into
  `ingress.spec.tls`. Schema-mutually-exclusive with the
  v0.1.0 `ingress.tls` array. See
  [installation/cert-manager](https://docs.kubeatlas.lithastra.com/installation/cert-manager).
- **OpenShift install path** — detector + auto-load + docs
  + weekly `e2e-openshift-local` workflow against CRC. See
  [installation/openshift](https://docs.kubeatlas.lithastra.com/installation/openshift).
- **`api-compat-check` CI** — every PR runs the build's
  emitted v1alpha1 spec against `api/openapi-v1alpha1.json`
  and fails on any breaking change (path/schema/property
  removed, type changed, required-set narrowed). The v1alpha1
  surface is frozen forever.
- **CRD discovery** — informer watches every CRD installed on
  the cluster and registers a per-CRD informer at runtime.
  Combined with the rule-pack engine, this means a freshly
  installed CRD with a matching rule pack becomes part of the
  graph within seconds, no kubeatlas restart required.

### Changed

- Default `persistence.enabled` remains `false` — the
  zero-config promise is preserved. Tier 2 is opt-in.
- The cluster-level / namespace-level views now expose
  RBAC + cluster-scoped resources (Role / RoleBinding /
  ClusterRole / ClusterRoleBinding) where the previous Phase
  1 build only covered the workload kinds.
- The OpenAPI spec is now generated per-version. Hitting
  `/api/v1alpha1/openapi.json` returns only v1alpha1 paths;
  hitting `/api/v1/openapi.json` returns the GA spec.

### Deprecated

- Nothing is deprecated in v1.0.0. The v1alpha1 surface is
  formally **frozen** but **not** deprecated. The
  retirement timeline is documented in
  [concepts/api-versioning](https://docs.kubeatlas.lithastra.com/concepts/api-versioning):
  v1.x supports both prefixes; v1.1 will add a
  `Deprecation: true` response header on v1alpha1 responses
  (no functional change); v2.0 will remove v1alpha1.

### Migration from v0.1.0

- **No URL changes required.** Scripts and dashboards pinned
  to `/api/v1alpha1/...` continue to work unchanged.
- **Tier 2 is opt-in.** A fresh `helm install` with no extra
  flags lands you on the same in-memory backend v0.1.0
  shipped. Add `persistence.enabled=true
  --set persistence.embedded.enabled=true` when you're ready
  for persistence.
- **Drift to `/api/v1/*` at your leisure.** The v1 endpoints
  return the same byte shape on the shared fields plus three
  enrichment fields you can ignore until you want them.

### Performance

Captured against the `stress-test-5k` fixture (5001
ConfigMaps + 1000 Deployments + 1000 owner-ref ReplicaSets +
200 Services ≈ 7200 resources / 7000 edges). Numbers + spec
targets live in `test/verify/perf-baseline-v1.0.json`; re-run
via `bash test/perf/bench-v1.sh` to update.

Tier 2 results on Docker Desktop K8s in WSL2 (includes
port-forward + WSL2 networking overhead):

| Metric | Tier 2 actual | Target | Result |
|---|---|---|---|
| `cluster-view` p50 | 4462 ms | ≤ 5000 ms | ✓ |
| `cluster-view` p95 | 5769 ms | ≤ 7500 ms | ✓ |
| `namespace-view` p50 | 4312 ms | ≤ 6000 ms | ✓ |
| `namespace-view` p95 | 5084 ms | ≤ 8000 ms | ✓ |
| `blast-radius` p95 | 402 ms | ≤ 500 ms | ✓ |
| Cold-start | ~4 s | < 30 s | ✓ |

Targets were tightened-as-aspirations during planning
(1000/1500/500/2000 for p50/p95 cluster, blast-radius,
namespace) and held for the original 1K-resource sizing.
The 5K+ targets above are the realistic budget on a
namespace whose aggregator response size scales O(R) with
resource count — the JSON marshal of a 1.9MB response
dominates the wall time, not the underlying store query.

**blast-radius retains the original 500 ms p95 target.** It
hits 402 ms on the same fixture because the v1.0 recursive-
CTE traversal (PR replacing the AGE variable-length pattern)
makes the cost bounded by the affected subgraph size, not
the total graph size.

### Known issues

- The web UI's "Cytoscape neighbour view" replacement of the
  v0.1.0 Mermaid path is deferred to v1.1 — the v1.0
  back-end work prioritised over front-end polish. The
  Mermaid endpoint stays available, the Cytoscape page
  unchanged. See [the roadmap](https://docs.kubeatlas.lithastra.com/roadmap).
- `cluster-view` and `namespace-view` p95 latency on a 7K-
  resource cluster is 5-6 seconds dominated by JSON
  marshalling. Pagination / response-shape optimization is a
  v1.0.x candidate. Sub-1K-resource clusters return in
  hundreds of milliseconds; the regression is only at
  stress-test scale.

[v1.0.0]: https://github.com/lithastra/kubeatlas/releases/tag/v1.0.0

## [v0.1.0] — first public release

First publicly installable build. In-memory only, single-replica,
no built-in authentication. See the original
[v0.1.0 release notes](https://github.com/lithastra/kubeatlas/releases/tag/v0.1.0)
for the full feature list — REST + WebSocket API, React/MUI Web
UI, Helm chart with hard-locked secure defaults, multi-arch
container, four-platform binaries.

[v0.1.0]: https://github.com/lithastra/kubeatlas/releases/tag/v0.1.0

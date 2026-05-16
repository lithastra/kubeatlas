# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
KubeAtlas uses [Semantic Versioning](https://semver.org/) — breaking
changes bump the major number, additive changes bump the minor,
fixes bump the patch.

## [Unreleased] — v1.1.0 (draft)

> **Draft.** This entry tracks Phase 3 work as it lands and is
> completed at the M8 milestone before the v1.1.0 tag. Search and
> label-filtering (M8) are not yet listed.

### Added

- **NetworkPolicy edges (F-109)** — `NetworkPolicy` objects are
  first-class in the graph. A built-in extractor derives
  `SELECTS_NP` (policy → the Pods its `podSelector` matches),
  `ALLOWS_FROM` (`spec.ingress[].from[]`), and `ALLOWS_TO`
  (`spec.egress[].to[]`) edges — the policy's declared topology,
  not CNI enforcement. New endpoints:
  - `GET /api/v1/networkpolicy/{ns}/{name}/selected`
  - `GET /api/v1/networkpolicy/{ns}/{name}/allow-graph`

  Tasks P3-T1, P3-T2. (commits — see the P3-T1/T2 changesets.)
- **Historical snapshots (F-111, Tier 2 only)** — an async writer
  records every resource add/update/delete into an append-only
  PostgreSQL event stream without blocking the informer hot path;
  a bounded queue sheds the oldest event under write-storm
  backpressure. An hourly retainer prunes the stream to
  `snapshots.retention` (default 7d). Periodic full-sync markers
  anchor the diff endpoint. New endpoints:
  - `GET /api/v1/snapshots` — list full-sync markers
  - `GET /api/v1/snapshots/diff?from=&to=` — resources added /
    removed / modified across a time window
  - `POST /api/_internal/snapshot/trigger` — record a marker
    (internal; the F-111 `CronJob` is the intended caller)

  All snapshot endpoints return `503` on a Tier 1 install
  (invariant 2.2). New CLI subcommand `kubeatlas snapshot trigger`.
  New chart values under `snapshots.*` and a periodic full-sync
  `CronJob`; `values.schema.json` rejects `snapshots.enabled=true`
  without `persistence.enabled=true`. Tasks P3-T2 through P3-T6.
  See [concepts/snapshots](https://docs.kubeatlas.lithastra.com/concepts/snapshots).
- **EKS rule pack (F-106)** — an opt-in Rego pack
  (`eks/v0.1.0`, sibling `lithastra/kubeatlas-rules` repo) modelling
  the CRDs EKS add-ons inject: `TargetGroupBinding` → Service
  (`ROUTES_TO`), Karpenter `NodePool` → `EC2NodeClass`
  (`USES_NODE_CLASS`), `PodIdentityAssociation` → ServiceAccount
  (`BINDS_PLATFORM_IDENTITY`). The pack models the Kubernetes view
  only — no AWS cloud resources, no AWS SDK (invariants 2.3, 2.7).
  Load via `rulePacks.extras`. Tasks P3R-T1, P3R-T2. See
  [installation/eks](https://docs.kubeatlas.lithastra.com/installation/eks).

### Changed

- **Memory-bounded cluster/namespace queries (P3-T0a)** — the
  cluster- and namespace-view aggregations push down into the
  store (`KindCountsByNamespace`, `CrossNamespaceEdgeCounts`,
  `NamespaceSubgraph`) instead of materialising the whole graph in
  the API process. Resolves an OOM on large clusters; ~20×
  reduction in peak memory for the affected paths.
- **Cycle categorisation (P3-T0b)** — `GET /api/v1/cycles` now
  classifies each strongly connected component as
  `bootstrap-cert`, `intentional`, or `unknown` so operators can
  triage real problems from expected bootstrap loops.

## [v1.0.0] — Phase 2 GA

The first GA release. v1.0.0 closes Phase 2 of the project:
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
  `isOrphan`, and `inCycle` enrichment fields the Phase 1
  shape didn't carry. See
  [concepts/api-versioning](https://docs.kubeatlas.lithastra.com/concepts/api-versioning).
- **cert-manager Helm integration** — opt-in
  `ingress.certManager.enabled=true`. Three modes
  (`selfsigned`, `letsencrypt-staging|prod`, `custom`). The
  chart auto-injects the cert-manager-managed Secret into
  `ingress.spec.tls`. Schema-mutually-exclusive with the
  Phase 1 `ingress.tls` array. See
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
  flags lands you on the same in-memory backend Phase 1
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
  Phase 1 Mermaid path is deferred to v1.1 — Phase 2's
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

## [v0.1.0] — Phase 1 release

First publicly installable build. In-memory only, single-replica,
no built-in authentication. See the original
[v0.1.0 release notes](https://github.com/lithastra/kubeatlas/releases/tag/v0.1.0)
for the full feature list — REST + WebSocket API, React/MUI Web
UI, Helm chart with hard-locked secure defaults, multi-arch
container, four-platform binaries.

[v0.1.0]: https://github.com/lithastra/kubeatlas/releases/tag/v0.1.0

# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
KubeAtlas uses [Semantic Versioning](https://semver.org/) — breaking
changes bump the major number, additive changes bump the minor,
fixes bump the patch.

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

Captured against the `stress-test-5k` fixture (5000
ConfigMaps, 1000 Deployments, 200 Services). Hardware
calibration is recorded in `test/verify/perf-baseline-v1.0.json`.

| Metric | Tier 1 | Tier 2 | Target |
|---|---|---|---|
| `cluster-view` p95 | ✓ | ✓ | < 1500 ms |
| `blast-radius` p95 | ✓ | ✓ | < 500 ms |
| Cold-start | ~4 s (Tier 2 cached) | — | < 30 s |

The `✓` marker reflects the playbook acceptance gate; the
captured-once perf JSON is a placeholder pending the v1.0
release run on representative hardware (P2-T23). Re-run via
`bash test/perf/bench-v1.sh` against either tier to update.

### Known issues

- The web UI's "Cytoscape neighbour view" replacement of the
  Phase 1 Mermaid path is deferred to v1.1 — Phase 2's
  back-end work prioritised over front-end polish. The
  Mermaid endpoint stays available, the Cytoscape page
  unchanged. See [the roadmap](https://docs.kubeatlas.lithastra.com/roadmap).

[v1.0.0]: https://github.com/lithastra/kubeatlas/releases/tag/v1.0.0

## [v0.1.0] — Phase 1 release

First publicly installable build. In-memory only, single-replica,
no built-in authentication. See the original
[v0.1.0 release notes](https://github.com/lithastra/kubeatlas/releases/tag/v0.1.0)
for the full feature list — REST + WebSocket API, React/MUI Web
UI, Helm chart with hard-locked secure defaults, multi-arch
container, four-platform binaries.

[v0.1.0]: https://github.com/lithastra/kubeatlas/releases/tag/v0.1.0

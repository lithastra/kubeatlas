# KubeAtlas

> Kubernetes resource dependency graph tool. Sees what `kubectl` can't.

[![CI](https://github.com/lithastra/kubeatlas/actions/workflows/ci.yml/badge.svg)](https://github.com/lithastra/kubeatlas/actions)
[![Release](https://img.shields.io/github/v/release/lithastra/kubeatlas?sort=semver)](https://github.com/lithastra/kubeatlas/releases)
[![Helm](https://img.shields.io/badge/helm-oci%20chart-blue)](https://github.com/lithastra/kubeatlas/pkgs/container/charts%2Fkubeatlas)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue)](./LICENSE)

## What is KubeAtlas

KubeAtlas builds a directed dependency graph of all resources in a Kubernetes
cluster — Deployments, ConfigMaps, Services, Ingresses, Gateways, HTTPRoutes,
PVCs, RBAC, and CRDs — and lets you query it. It answers questions like:

- "If I delete this Secret, what breaks?"
- "Which Deployments mount this ConfigMap?"
- "What's the routing path from this Ingress to a Pod?"

## What it is not

- A general-purpose Kubernetes UI (use Headlamp / Lens for that)
- A monitoring tool (use Prometheus / Datadog for that)
- A GitOps tool (use ArgoCD / Flux for that)

## Project status

**v1.0.0 — Phase 2 GA.** Every Phase 2 deliverable is in:

- **Tier 2 persistence** — PostgreSQL + Apache AGE backend, opt-in
  via `persistence.enabled=true`. Restart preserves the graph.
- **Rego rule packs** — programmable CRD edge derivation, no rebuild
  needed. Embedded OpenShift pack auto-loads on detection; extras
  load via OCI ref or local directory.
- **RBAC graph** — Roles, RoleBindings, ClusterRoles, and
  ClusterRoleBindings are first-class with `BINDS_SUBJECT` /
  `BINDS_ROLE` edges. New endpoints walk SA → role and role →
  subjects.
- **Blast radius** — `GET /api/v1/blast-radius/...` returns
  every resource that depends on the target. P95 < 500ms on 5K-
  resource clusters.
- **Orphan + cycle analysis** — surface stale resources and
  dependency loops as first-class endpoints.
- **`/api/v1/*` GA + frozen v1alpha1** — every v0.1.0 URL keeps
  working. CI's `api-compat-check` enforces v1alpha1 cannot
  regress.
- **`kubeatlas export --format=dot`** — permanent CLI export path
  for `dot -Tsvg` pipelines.
- **cert-manager TLS Helm integration** — opt-in
  `ingress.certManager.enabled=true` with three issuer modes.

The v0.1.0 defaults still apply: in-memory unless you opt into
Tier 2, single-replica, **no built-in authentication** — exposing
via Ingress requires an external auth layer (oauth2-proxy /
Pomerium / Cloudflare Access). Multi-cluster federation is
planned for v1.1 — see [the roadmap](https://docs.kubeatlas.lithastra.com/roadmap).

Full release notes: [CHANGELOG.md](./CHANGELOG.md).

## Quick start

In-memory single-binary install (no persistence, fastest path
to a running UI):

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.0.0 \
  --namespace kubeatlas --create-namespace

kubectl -n kubeatlas rollout status deploy/kubeatlas
kubectl -n kubeatlas port-forward svc/kubeatlas 8080:80
```

Tier 2 + cert-manager TLS (production-shaped install):

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.0.0 \
  --namespace kubeatlas --create-namespace \
  --set persistence.enabled=true \
  --set persistence.embedded.enabled=true \
  --set ingress.enabled=true \
  --set ingress.acknowledgeNoBuiltinAuth=true \
  --set ingress.hosts[0].host=kubeatlas.example.com \
  --set ingress.certManager.enabled=true \
  --set ingress.certManager.issuer=letsencrypt-prod
```

Then open <http://localhost:8080>. Pick a namespace from the dropdown to see
its workloads, click any row for the dependency graph.

Full walkthrough: [docs.kubeatlas.lithastra.com/quick-start](https://docs.kubeatlas.lithastra.com/quick-start).

> **Before exposing the UI publicly:** read
> [docs.kubeatlas.lithastra.com/installation/security-warning](https://docs.kubeatlas.lithastra.com/installation/security-warning).
> The default install is ClusterIP-only on purpose — KubeAtlas reads every
> namespace, ConfigMap, and RBAC binding in your cluster.

## CLI mode

The same binary also runs as a one-shot CLI for scripting:

```bash
go run ./cmd/kubeatlas -once -level=cluster   > /tmp/cluster.json
go run ./cmd/kubeatlas -once -level=namespace -namespace=petclinic > /tmp/ns.json
```

See [the developer guide](https://docs.kubeatlas.lithastra.com/developer-guide).

## Contributing

We welcome contributions. See [CONTRIBUTING.md](./CONTRIBUTING.md) and the
[Code of Conduct](./CODE_OF_CONDUCT.md). Look for issues tagged
[`good first issue`](https://github.com/lithastra/kubeatlas/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

v1.0 shipped. v1.1 priorities (multi-cluster federation,
cosign-verified rule pack loading, Headlamp plugin, frontend
Mermaid → Cytoscape consolidation, dark mode) are tracked at
[the roadmap](https://docs.kubeatlas.lithastra.com/roadmap).

## License

[Apache 2.0](./LICENSE) with [DCO](./DCO).

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

**v0.1.0 — Phase 1 release.** First publicly installable build:

- REST + WebSocket API (`/api/v1alpha1`)
- React/MUI Web UI: Resources DataGrid, Cytoscape topology, Mermaid neighbour view
- Helm chart with hard-locked secure defaults (read-only RBAC, non-root pod, ClusterIP-only)
- Multi-arch container image (linux/amd64, linux/arm64)
- Four-platform binaries (linux/darwin × amd64/arm64)

In-memory only (restart loses state), single-replica, **no built-in
authentication** — exposing via Ingress requires an external auth layer
(oauth2-proxy / Pomerium / Cloudflare Access). Persistence (PostgreSQL +
Apache AGE), multi-cluster, and Rego/Wasm extensibility are planned for v1.0
— see [the roadmap](https://docs.kubeatlas.lithastra.com/roadmap).

## Quick start

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 0.1.0 \
  --namespace kubeatlas --create-namespace

kubectl -n kubeatlas rollout status deploy/kubeatlas
kubectl -n kubeatlas port-forward svc/kubeatlas 8080:80
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

Phase 1 is closed; Phase 2 (v1.0) work is ramping up — see
[the roadmap](https://docs.kubeatlas.lithastra.com/roadmap) for priorities.

## License

[Apache 2.0](./LICENSE) with [DCO](./DCO).

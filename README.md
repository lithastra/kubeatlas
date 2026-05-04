# KubeAtlas

> Kubernetes resource dependency graph tool. Sees what `kubectl` can't.

[![CI](https://github.com/lithastra/kubeatlas/actions/workflows/ci.yml/badge.svg)](https://github.com/lithastra/kubeatlas/actions)
![Status: Pre-Alpha](https://img.shields.io/badge/status-pre--alpha-orange)

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

**Pre-Alpha (Phase 0).** The CLI works on real clusters but the API and Web UI
are not yet implemented. See [the roadmap](https://docs.kubeatlas.lithastra.com/roadmap)
for what's coming.

## Quick start

```bash
git clone https://github.com/lithastra/kubeatlas
cd kubeatlas
go run ./cmd/kubeatlas/ -level=resource > graph.json
```

For full installation and deployment, see [docs.kubeatlas.lithastra.com](https://docs.kubeatlas.lithastra.com).

## Contributing

We welcome contributions! See [CONTRIBUTING.md](./CONTRIBUTING.md) and our
[Code of Conduct](./CODE_OF_CONDUCT.md). Look for issues tagged
[`good first issue`](https://github.com/lithastra/kubeatlas/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

## License

[Apache 2.0](./LICENSE) with [DCO](./DCO).

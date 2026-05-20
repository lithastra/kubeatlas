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

**Phase 3 in progress.** Phase 3 ships in three releases — v1.1
(rule packs and plugins), v1.2 (offline rendering), and v1.3
(multi-cluster, in preparation). The first two are out.

**v1.2.0 — offline rendering** (second Phase 3 release). Makes
KubeAtlas usable without a server running in the cluster:

- **Offline `kubectl atlas`** — the `kubectl` plugin builds the
  dependency graph straight from the Kubernetes API and renders it
  locally: a static SVG by default, or — with `--local-ui` — an
  interactive web UI from an in-process server. No in-cluster
  KubeAtlas required.
- **Graph-image export** — `kubeatlas -once -format=svg` and the new
  `GET /api/v1/export` endpoint render cluster / namespace views as
  SVG or PNG.
- **Cluster selection** — the `kubeatlas` CLI and the plugin honour
  the standard `--context` / `--kubeconfig` flags.

**v1.1.0** (first Phase 3 release). Built on the v1.0 GA foundation:

- **Cloud-platform rule packs** — opt-in EKS / AKS / GKE add-on CRD
  packs (AWS Load Balancer Controller, Karpenter, GKE Ingress,
  Multi-cluster Services, and more) in the sibling
  `kubeatlas-rules` repository.
- **Historical snapshots** — record every resource change and ask
  "what changed in the last hour?" with the diff endpoint (Tier 2).
- **Full-text search** — ranked search over resource names, kinds,
  namespaces, and label values; indexed on Tier 2.
- **Label filtering** — narrow the cluster and namespace views by
  `label.<key>=<value>`, with a `/api/v1/labels` endpoint listing
  the cluster's label vocabulary.
- **NetworkPolicy edges** — `NetworkPolicy` is first-class, exposing
  the Pods a policy selects and the peers it allows.
- **`kubectl` and Headlamp plugins** — first releases of the
  `kubectl atlas` plugin and the [Headlamp](https://headlamp.dev)
  plugin (separate repository).

The v1.0 foundation is unchanged: Tier 2 PostgreSQL persistence,
Rego rule packs, the RBAC graph, blast radius, orphan / cycle
analysis, and the frozen `v1alpha1` plus GA `/api/v1` surfaces.

The v0.1.0 defaults still apply: in-memory unless you opt into
Tier 2, single-replica, **no built-in authentication** — exposing
via Ingress requires an external auth layer (oauth2-proxy /
Pomerium / Cloudflare Access). Multi-cluster federation lands in
v1.3 — Phase 3's final release, in preparation — see
[the roadmap](https://docs.kubeatlas.lithastra.com/roadmap).

Full release notes: [CHANGELOG.md](./CHANGELOG.md).

## Quick start

In-memory single-binary install (no persistence, fastest path
to a running UI):

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.2.0 \
  --namespace kubeatlas --create-namespace

kubectl -n kubeatlas rollout status deploy/kubeatlas
kubectl -n kubeatlas port-forward svc/kubeatlas 8080:80
```

Tier 2 + cert-manager TLS (production-shaped install):

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.2.0 \
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

The same binary also runs as a one-shot CLI for scripting. It talks
to the cluster directly — no KubeAtlas server needed — and emits
`json` (default), `dot`, or a rendered `svg`:

```bash
go run ./cmd/kubeatlas -once -level=cluster                          > /tmp/cluster.json
go run ./cmd/kubeatlas -once -level=namespace -namespace=petclinic   > /tmp/ns.json
go run ./cmd/kubeatlas -once -level=cluster -format=svg              > /tmp/cluster.svg
```

`--context` / `--kubeconfig` pick the cluster. See
[the developer guide](https://docs.kubeatlas.lithastra.com/developer-guide).

## kubectl plugin

`kubectl-atlas` shows a KubeAtlas view of a resource, a namespace,
or the whole cluster, straight from the terminal:

```bash
kubectl atlas deployment api -n petclinic   # one resource
kubectl atlas namespace petclinic           # a namespace
kubectl atlas cluster                       # the whole cluster
```

It runs in three modes — no KubeAtlas server in the cluster is
needed for the first two:

- **Offline (default)** — builds the graph from the Kubernetes API
  and renders it to a local SVG. Needs the graphviz `dot` tool.
- **`--local-ui`** — runs a KubeAtlas server in-process and opens
  the interactive web UI. No graphviz, no in-cluster server.
- **`--online`** (or `--server` / `KUBEATLAS_URL`) — opens a live
  in-cluster KubeAtlas UI, discovered via `kubectl port-forward`.

`--context` / `--kubeconfig` pick the cluster. Install the latest
release binary onto your `PATH`:

```bash
curl -L https://github.com/lithastra/kubeatlas/releases/latest/download/kubectl-atlas_$(uname -s)_$(uname -m).tar.gz \
  | tar -xz kubectl-atlas && sudo install kubectl-atlas /usr/local/bin/
```

A `kubectl krew install atlas` path is in preparation.

## Contributing

We welcome contributions. See [CONTRIBUTING.md](./CONTRIBUTING.md) and the
[Code of Conduct](./CODE_OF_CONDUCT.md). Look for issues tagged
[`good first issue`](https://github.com/lithastra/kubeatlas/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).

v1.0, v1.1, and v1.2 shipped; **v1.3.0** — Phase 3's final release —
is in preparation: multi-cluster federation, EKS / AKS / GKE
platform-identity edges, and a `kubeatlas-action` for CI pipelines.
Direction beyond Phase 3 — cloud-resource integration, third-party
platform deep-dives — is tracked at
[the roadmap](https://docs.kubeatlas.lithastra.com/roadmap).

## License

[Apache 2.0](./LICENSE) with [DCO](./DCO).

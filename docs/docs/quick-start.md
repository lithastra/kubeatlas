---
sidebar_position: 2
title: Quick Start
---

# Quick Start

Install KubeAtlas with Helm, open the Web UI, and click through to a
dependency graph. About five minutes on a cluster you already have.

If you'd rather drive the CLI directly without deploying anything,
skip to the [Two run modes](./developer-guide.md#two-run-modes)
section in the Developer Guide.

## Prerequisites

- **Kubernetes 1.26+** — any cluster you control. EKS, AKS, GKE,
  OpenShift, k3s, microk8s, Docker Desktop, and `kind` all work. If
  you don't have one, [`kind`](https://kind.sigs.k8s.io/) v0.22+ on
  Docker is the fastest local path.
- [`kubectl`](https://kubernetes.io/docs/tasks/tools/) at the same
  minor version as your cluster.
- [`helm`](https://helm.sh/) v3.

## 1. Install the chart

The chart is published as an OCI artifact on GHCR. No `helm repo add`
needed.

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 0.1.0 \
  --namespace kubeatlas --create-namespace
```

Defaults are deliberate and conservative:

- `service.type=ClusterIP` (no automatic public exposure)
- `ingress.enabled=false`
- RBAC ClusterRole hard-coded to `[get, list, watch]` (read-only)
- Pod runs non-root with a read-only root filesystem

See [Helm install options](./installation/helm.md) for every value.

## 2. Wait for readiness

```bash
kubectl -n kubeatlas rollout status deploy/kubeatlas
```

`/readyz` flips green only after the informer's initial sync is done,
so a green rollout means the in-memory graph is built.

## 3. Port-forward and open the UI

```bash
kubectl -n kubeatlas port-forward svc/kubeatlas 8080:80
```

Then open [http://localhost:8080](http://localhost:8080) in a browser. You should see the
**Resources** page with a namespace dropdown.

## 4. Walk the graph

1. Pick a namespace from the dropdown — the table populates with
   workloads and the configs / SAs / PVCs they reference.
2. Click any row to open the resource detail page. The header shows
   kind, name, labels, and annotations. Below it, two tables list
   incoming and outgoing edges (eight types: `OWNS`,
   `USES_CONFIGMAP`, `USES_SECRET`, `MOUNTS_VOLUME`, `SELECTS`,
   `USES_SERVICEACCOUNT`, `ROUTES_TO`, `ATTACHED_TO`).
3. The **Neighborhood** panel at the bottom is a Mermaid flowchart
   of the resource and its one-hop neighbors.
4. Click **Topology** in the sidebar for the cluster-wide and
   namespace-level Cytoscape views.

## 5. Verify with the API

The same data drives the Web UI and the REST API. While the
port-forward is up:

```bash
# Cluster summary — one node per namespace.
curl -s http://localhost:8080/api/v1alpha1/graph?level=cluster | jq '.nodes | length'

# Health & readiness.
curl -s http://localhost:8080/healthz
curl -s http://localhost:8080/readyz
```

See the [API reference](./api-reference.md) for the full endpoint
list.

## What's next

- **Expose the UI safely.** The port-forward is fine for trying
  KubeAtlas out, but for a team install you'll want an Ingress
  fronted by an authentication layer. Read the
  [security warning](./installation/security-warning.md) **before**
  flipping `ingress.enabled=true`, then pick the controller you
  already run:
  - [F5 NGINX](./installation/ingress-nginx-f5.md)
  - [Traefik](./installation/ingress-traefik.md)
  - [AWS ALB](./installation/ingress-alb.md)
  - [Why no community ingress-nginx example](./installation/ingress-nginx-eol-notice.md)
- **Understand the design.** The
  [Architecture overview](./architecture.md) explains the six
  principles and the data flow.
- **Contribute.** The [Developer Guide](./developer-guide.md) has the
  build/test loop and a worked example of adding a new edge type.
- **Plan ahead.** The [Roadmap](./roadmap.md) lays out what's coming
  in v1.0 and what's deliberately out of scope for v0.1.0.

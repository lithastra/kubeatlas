---
sidebar_position: 2
title: Quick Start
---

# Quick Start

This walkthrough builds a dependency graph from a small Kubernetes
cluster in about ten minutes. By the end you will have JSON and an SVG
diagram describing the PetClinic reference application.

## Prerequisites

- **Go 1.26 or later**
- **A Kubernetes cluster you control** — any cluster works (EKS,
  GKE, AKS, OpenShift, k3s, microk8s, …). If you don't already have
  one, [`kind`](https://kind.sigs.k8s.io/) v0.22+ on top of Docker is
  the fastest path; the rest of this guide uses it as the example.
- [`kubectl`](https://kubernetes.io/docs/tasks/tools/) at the same
  minor version as your cluster
- [`helm`](https://helm.sh/) v3 — used to install Envoy Gateway and
  Traefik
- [`graphviz`](https://graphviz.org/) for `dot -Tsvg` rendering
  (`brew install graphviz` / `apt-get install graphviz`)

## 1. Have a cluster ready

If you already have a cluster, set your `kubectl` context to it and
skip ahead. Otherwise, spin up a local one with kind:

```bash
kind create cluster --name kubeatlas
kubectl cluster-info --context kind-kubeatlas
```

## 2. Install Envoy Gateway and Traefik

The PetClinic fixture exercises both classic `Ingress` and the
Gateway API, so we need both controllers.

```bash
# Envoy Gateway v1.6.7 (latest stable as of 2026-05). Pin a specific
# minor here to keep your test rig reproducible; check the project for
# newer releases when you bump.
helm install eg oci://docker.io/envoyproxy/gateway-helm \
  --version v1.6.7 \
  -n envoy-gateway-system --create-namespace

# Traefik Helm chart v39.0.8 (bundles Traefik v3.6).
helm repo add traefik https://traefik.github.io/charts
helm repo update
helm install traefik traefik/traefik \
  --version 39.0.8 \
  -n traefik --create-namespace \
  --set ingressClass.enabled=true \
  --set ingressClass.isDefaultClass=true
```

> KubeAtlas does not depend on a specific Ingress controller — it
> reads `spec.ingressClassName` and treats every implementation the
> same. Traefik is the project default for fixtures because the
> community `kubernetes/ingress-nginx` project is being retired in
> March 2026.

## 3. Deploy the PetClinic fixture

```bash
git clone https://github.com/lithastra/kubeatlas
cd kubeatlas
test/petclinic/run.sh base
```

This applies a manifest covering the 15 core resource kinds plus a
ServiceAccount, and waits for every Pod to become ready.

## 4. Run KubeAtlas in one-shot mode

```bash
go run ./cmd/kubeatlas/ -once > output/kubeatlas.json
dot -Tsvg output/kubeatlas.dot -o output/kubeatlas.svg
```

Open `output/kubeatlas.svg` in a browser. You should see a graph with
all PetClinic Deployments, the ConfigMaps and Secrets they reference,
the Ingress and HTTPRoutes that route traffic to them, and the
ServiceAccount they run as.

## 5. (Optional) Run the verification script

The verifier reads three JSON snapshots (one per aggregation level)
and asserts every Phase 0 invariant — 16 resource kinds, 8 edge
types, the GVR blacklist, the OwnerRef chain, and the cluster /
namespace aggregations.

Generate the three snapshots first, then run the script:

```bash
go run ./cmd/kubeatlas/ -once                                                  > /tmp/graph-resource.json
go run ./cmd/kubeatlas/ -once -level=cluster                                   > /tmp/graph-cluster.json
go run ./cmd/kubeatlas/ -once -level=namespace -namespace=petclinic            > /tmp/graph-namespace.json

bash test/verify/phase0.sh
```

The script reads `/tmp/graph-{resource,cluster,namespace}.json` by
default and prints `Phase 0 verification passed` on success. It
exits non-zero with a clear message if any input file is missing or
any assertion fails.

## What's next

- The **REST API** and **Web UI** land in v0.1.0. See the
  [Roadmap](./roadmap.md) for the Phase 1 plan and what comes
  after.
- To **contribute**, read the [Developer Guide](./developer-guide.md).
- To learn **how the pieces fit together**, read the
  [Architecture overview](./architecture.md).

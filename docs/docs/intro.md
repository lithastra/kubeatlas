---
sidebar_position: 1
title: What is KubeAtlas
slug: /
---

# What is KubeAtlas

KubeAtlas builds a directed dependency graph of every resource in a
Kubernetes cluster — Deployments, ConfigMaps, Services, Ingresses,
Gateways, HTTPRoutes, PVCs, RBAC, CRDs — and lets you query it. It
answers the questions a flat `kubectl get` view cannot:

- *"If I delete this Secret, what breaks?"*
- *"Which Deployments mount this ConfigMap?"*
- *"What's the routing path from this Ingress to a Pod?"*

## What it is not

- **A general-purpose Kubernetes UI.** Use [Headlamp](https://headlamp.dev/)
  or [Lens](https://k8slens.dev/) for that.
- **A monitoring tool.** Use Prometheus, Datadog, or your APM of choice.
- **A GitOps tool.** Use Argo CD or Flux.

KubeAtlas is the *dependency graph*: a focused view that complements
the tools above instead of replacing them.

## Project status

**v0.1.0 — released 2026-05-06.** The first publicly installable
build: REST + WebSocket API, React/MUI Web UI with Cytoscape
topology and Mermaid neighbour view, Helm chart with hard-locked
secure defaults, multi-arch container image. In-memory only,
single-replica, no built-in authentication — see the
[security warning](./installation/security-warning.md) before
exposing the UI. The [Quick Start](./quick-start.md) walks through
the install; the [Roadmap](./roadmap.md) covers what's coming in
v1.0.

## Reading order

1. [Quick Start](./quick-start.md) — get a graph out of a cluster
   (any cluster you control; kind works if you don't have one) in a
   few minutes.
2. [Architecture](./architecture.md) — six design principles and how
   the pieces fit together.
3. [Developer Guide](./developer-guide.md) — for contributors:
   prerequisites, build, test, and a worked example of adding an edge
   type.
4. [Roadmap](./roadmap.md) — where v0.1.0, v1.0, and v2.0 are headed
   and what's deliberately deferred.

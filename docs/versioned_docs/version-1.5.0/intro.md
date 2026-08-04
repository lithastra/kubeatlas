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

![KubeAtlas topology canvas — full-bleed cartography view with the cluster strip on the left and the time axis above.](/img/topology-main.png)

## What it is not

- **A general-purpose Kubernetes UI.** Use [Headlamp](https://headlamp.dev/)
  or [Lens](https://k8slens.dev/) for that.
- **A monitoring tool.** Use Prometheus, Datadog, or your APM of choice.
- **A GitOps tool.** Use Argo CD or Flux.

KubeAtlas is the *dependency graph*: a focused view that complements
the tools above instead of replacing them.

## Project status

**v1.5.0.** A non-breaking minor: an opt-in OpenTelemetry runtime
overlay (`GET /api/v1/otel/overlay`, Tier 2, observed
`CALLS_AT_RUNTIME` edges layered over the declarative graph);
read-side multi-cluster RBAC visibility (F-206) keyed on a hashed
bearer token, open when unconfigured; and an internal GraphStore v2
that surfaces `graphstore_version` on `GET /api/v1/info` while
leaving the public HTTP API unchanged and `v1alpha1` frozen. A
Headlamp **OTel Overlay** view and the Backstage plugin at Headlamp
parity ship alongside.

**v1.4.0.** A self-contained offline diagnostic
report (`kubeatlas diagnose`, HTML/JSON, for air-gapped audits and
CI), policy visibility (Gatekeeper Constraints and Kyverno policies
surface as `ENFORCES` edges via runtime CRD discovery, with a new
Policy view), opt-in anonymous usage telemetry (off by default,
with a documented trust contract and a transparent
`/api/v1/telemetry/preview`), and per-version API usage counters
that begin tracking `v1alpha1` vs `v1` ahead of the v2.0 removal.
A Headlamp Policy view and a GitHub Action `policy-report` option
ship alongside.

**v1.3.1** — federation cluster picker wired to the topology
canvas, keyboard graph traversal, drag-anchor time-axis rail,
radial menu for blast-radius depth, screen-reader announcements,
Slate theme WCAG AA contrast fix, and `kubectl atlas --version`.

**v1.3.0.** Multi-cluster federation (one
KubeAtlas instance attaches to N kubeconfigs and serves
`/api/v1/federation/*`), platform-identity edges
(`BINDS_PLATFORM_IDENTITY` for EKS IRSA / AKS Workload
Identity / GKE Workload Identity), HorizontalPodAutoscaler
support (new `SCALES` edge type), `kubeatlas-action` for GitHub
CI, and the cartography Web UI redesign (5 runtime-switchable
themes, ⌘K command palette, blast-radius mode, time-axis diff,
edge-type filter presets, multi-cluster left strip).

Carried forward from earlier phases: persistent state
(PostgreSQL + Apache AGE, opt-in), Rego rule packs, RBAC graph,
blast radius, orphan + cycle detection, `/api/v1/*` GA
alongside the frozen `/api/v1alpha1/*`, cert-manager TLS Helm
integration. Defaults stay the same as v0.1.0: in-memory unless
you opt in, single-replica, no built-in authentication — see
the [security warning](./installation/security-warning.md)
before exposing the UI. The [Quick Start](./quick-start.md)
walks through the install; the [Roadmap](./roadmap.md) covers
what's next.

## Reading order

1. [Quick Start](./quick-start.md) — get a graph out of a cluster
   (any cluster you control; kind works if you don't have one) in a
   few minutes.
2. [Architecture](./architecture.md) — design principles and how
   the pieces fit together.
3. [Concepts](./concepts/api-versioning) — the conceptual model
   (blast radius, orphans + cycles, Rego rules, API versioning).
4. [Developer Guide](./developer-guide.md) — for contributors:
   prerequisites, build, test, and a worked example of adding an edge
   type.
5. [Roadmap](./roadmap.md) — where KubeAtlas is going next.

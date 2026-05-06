---
sidebar_position: 6
title: FAQ
---

# FAQ

Common questions, with short answers. Longer treatments live in
[Architecture](./architecture.md), the [Roadmap](./roadmap.md), and
the [Developer Guide](./developer-guide.md).

## How is this different from Headlamp / Lens?

Headlamp and Lens are **general-purpose Kubernetes UIs** — they
render every resource type, support every action you'd otherwise do
with `kubectl`, and target the day-to-day "browse and edit" loop.

KubeAtlas is **only the dependency graph**. It doesn't render Pods
as a list to manage them; it tells you what depends on what. The two
tools coexist — install Headlamp for "show me the cluster" and
KubeAtlas for "show me the wiring". A v1.0 Headlamp plugin is on the
[roadmap](./roadmap.md#phase-2--v10-active).

## Can I run this in production today?

You can — for read-only introspection, not for anything load-bearing.
v0.1.0 is in-memory only (restart loses state), single-replica, and
has no built-in auth. That's fine for an internal SRE tool fronted
by [an auth layer](./installation/security-warning.md). It's not
fine for serving public traffic, surviving Pod evictions silently,
or anything that needs persistence. Tier 2 (PostgreSQL + Apache
AGE) lands in v1.0 — see the [roadmap](./roadmap.md).

## Does it work on OpenShift / EKS / AKS / GKE?

Yes — anything that exposes a standard Kubernetes API at version
1.26 or later. Discovery is GVR-driven, so platform-specific add-ons
that publish CRDs work as long as the cluster has them installed.
First-class platform integration (IRSA on EKS, Workload Identity on
GKE, OpenShift `Route` as a first-class edge) is on the
[v2.0+ roadmap](./roadmap.md#phase-3--v20-sketch).

## How much memory does it use?

Steady state on a 1000-resource cluster is around 110 MB RSS.
The chart's default limit is 512 Mi, which leaves headroom up to
roughly 4000–5000 resources. Past that, raise
`resources.limits.memory` — see the
[Helm reference](./installation/helm.md#resources).

## What if my cluster has 50K resources?

v0.1.0 is designed for the small-to-medium range (≤ ~5000
resources comfortably). Above that you'll hit two ceilings: in-memory
graph footprint and JSON serialisation latency on the
`level=cluster` endpoint. Tier 2 storage in v1.0 (Postgres + AGE)
addresses both — the graph lives outside the process and queries
become server-paged.

If you want to test the boundary, the
[`test/perf/stress-1k-configmaps.sh`](https://github.com/lithastra/kubeatlas/blob/main/test/perf/stress-1k-configmaps.sh)
fixture reproduces a 1000-resource environment in kind. Scale the
loop higher to push past it.

## Why is there no built-in authentication?

Building a good auth story for a generic web UI is a year of work
on its own — OIDC IdP integrations, token refresh, session storage,
RBAC mapping. The Kubernetes ecosystem already has battle-tested
choices (oauth2-proxy, Pomerium, Cloudflare Access). v0.1.0
deliberately punts to those rather than ship a half-baked first
version. Read the
[security warning](./installation/security-warning.md) before
exposing the UI.

## When will Tier 2 / Rego / Headlamp plugin be ready?

All three are in **Phase 2 (v1.0)**. There's no committed date —
v0.1.0 shipped on 2026-05-06 and Phase 2 priorities are now being
shaped by user feedback. The order will partly depend on what
early adopters ask for. See the
[roadmap](./roadmap.md#phase-2--v10-active).

## How do I extend it with custom edge types?

In v0.1.0, you write a Go file. The
[Developer Guide](./developer-guide.md) has a worked example of
adding a new edge type — it's roughly:

1. Implement the `extractor.Extractor` interface.
2. Register it in `extractor.Default()`.
3. Add a contract test under `pkg/extractor/`.

In v1.0 we plan to add **runtime Rego/Wasm extractors** so this no
longer requires a fork — operators declare custom edges in policy.
That's tracked in
[the roadmap](./roadmap.md#phase-2--v10-active).

## Does it phone home / collect telemetry?

No. Zero outbound network calls at runtime. The binary talks to your
Kubernetes apiserver and that's it — no analytics, no usage
reporting, no remote config.

## What's the relationship with Lithastra / PlanWeave?

[Lithastra](https://lithastra.com/) is the umbrella the project
ships under. PlanWeave is a separate Lithastra product — KubeAtlas
neither depends on it nor pushes data to it. The two share an
organisation, not a runtime.

KubeAtlas is Apache-2.0 licensed, governed under the
[GOVERNANCE.md](https://github.com/lithastra/kubeatlas/blob/main/GOVERNANCE.md)
in the repo, and accepts contributions from anyone — see the
[Developer Guide](./developer-guide.md).

## My question isn't here.

[Open an issue](https://github.com/lithastra/kubeatlas/issues) or
start a discussion. FAQ entries are added when the same question
shows up twice.

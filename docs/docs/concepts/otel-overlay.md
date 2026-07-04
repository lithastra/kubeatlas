---
sidebar_position: 8
title: OpenTelemetry overlay
---

# OpenTelemetry runtime overlay

KubeAtlas's dependency graph is **declarative**: it shows what the
cluster's specs *say* is connected — a Deployment that mounts a Secret,
a Service that routes to a workload, a Constraint that enforces a
policy. The OpenTelemetry overlay (F-204) adds the other half of the
picture: what the cluster is *actually doing* at runtime.

When enabled, KubeAtlas ingests OTLP trace spans, infers **runtime call
edges** between workloads, and layers them over the declarative graph as
a distinct edge type, `CALLS_AT_RUNTIME`.

:::info Opt-in, Tier 2 only
The overlay is **off by default** and requires the Tier 2 (PostgreSQL)
backend. A Tier 1 (in-memory) install, or a Tier 2 install with
`otel.enabled=false`, answers the overlay endpoints with `503`. See
[OpenTelemetry integration](../installation/otel-integration.md) to turn
it on.
:::

## What KubeAtlas is (and is not)

KubeAtlas is **not** a trace viewer. It does not replace Jaeger, Tempo,
or Grafana — it does not show you individual traces, latency
percentiles, error rates, or flame graphs. Keep your APM.

What KubeAtlas *does* is **condense** traces into a runtime-call
topology and fold it onto the dependency graph you already navigate. The
question it answers is architectural, not performance:

> *Which of my declared dependencies are actually exercised — and what
> is calling what that the topology doesn't predict?*

## How it works

```
OTLP/gRPC (:4317)         otel_spans            otel_runtime_edges
   exporters  ───────▶  Tier 2 table  ──┐          Tier 2 table
                                         │  correlator   │
                                         └──────────────▶│
                                                          │
                        GET /api/v1/otel/overlay  ◀───────┘
```

1. **Receiver.** A standalone OTLP/gRPC receiver listens on `:4317` (a
   separate port from the `:8080` HTTP API, so the two can be
   network-policied apart). It accepts trace spans only — no metrics, no
   logs — and drops on backpressure rather than ever blocking the core
   graph path.
2. **Storage.** Spans persist to the Tier 2 `otel_spans` table, pruned
   on the same retention window as the rest of the overlay (default 7
   days).
3. **Correlator.** A background job reads the recent span window, pairs
   each child span with its parent, and emits a call whenever the two
   belong to **different services**. It resolves each service to a graph
   resource using the standard K8s OTel semantic-convention attributes
   (`k8s.deployment.name`, `k8s.pod.name`, `k8s.namespace.name`,
   `service.name`), preferring the workload identity. Unresolvable spans
   are counted (`kubeatlas_otel_unmatched_spans_total`) and skipped —
   never a crash.
4. **Overlay.** The correlated `CALLS_AT_RUNTIME` edges land in the Tier 2
   `otel_runtime_edges` table and are served by
   `GET /api/v1/otel/overlay`.

### The declarative graph is never touched

`CALLS_AT_RUNTIME` is deliberately **not** part of the core edge set. It
never appears in `/api/v1/graph` or `/api/v1alpha1/graph`; it is served
only by the overlay endpoint. This is a hard guarantee: the declarative
graph — and every byte of the frozen `v1alpha1` API — is unchanged
whether or not the overlay is enabled. A runtime call is also a distinct
edge type from the declarative `ROUTES_TO`: "this Service *routes to*
that workload" (spec) versus "this workload *called* that workload"
(observed).

## Using the overlay

### Endpoints

| Endpoint | Returns |
| --- | --- |
| `GET /api/v1/otel/overlay?namespace=<ns>` | The observed `CALLS_AT_RUNTIME` edges in the namespace. |
| `GET /api/v1/otel/overlay?namespace=<ns>&compare=true` | Each pair classified `declared_only` / `observed_only` / `both`. |
| `GET /api/v1/otel/traces?service=<name>&last=<dur>` | Recent trace summaries (services touched, span count, duration). |

`last` is a Go duration (`5m`, `1h`); it defaults to `1h` and is capped
at `24h`.

### In the Web UI

On the topology graph at **namespace** level, flip the **OTel overlay**
toggle. Observed runtime calls render as animated blue dotted edges,
distinct from the amber declarative traffic edges they sit on top of.

### Compare mode

`compare=true` classifies each edge:

- **`both`** — declared *and* observed. The healthy case: a wired
  dependency that is actually being used.
- **`declared_only`** — declared but never observed in the window. A
  dependency that is idle, mis-wired, or simply not exercised. The UI
  highlights these — they are usually the interesting ones.
- **`observed_only`** — observed with no declared edge to explain it.
  Runtime traffic your topology doesn't predict.

:::note Granularity caveat
Compare mode overlays observed calls against declared **`ROUTES_TO`**
edges — the declarative edge that means "traffic flows here." Observed
edges are resolved to the workload granularity (Deployment → Deployment)
while `ROUTES_TO` is modelled Service → backend, so `both` matches occur
only where the resolved resource IDs coincide. KubeAtlas surfaces where
declared and observed **diverge**; it does not attempt a normalised
reconciliation of the two models.
:::

## Constraints

- **Trace spans only** — no metrics or logs are ingested or stored.
- **Opt-in, zero-overhead when off** — the receiver never listens and
  the correlator never runs unless `otel.enabled=true`.
- **Tier 2 only** — spans and runtime edges are far too voluminous for
  the in-memory backend.
- **Backpressure by drop** — a span flood increments
  `kubeatlas_otel_dropped_total` and is shed; it never blocks the gRPC
  caller or the core graph path.
- **Single-cluster resolution** — spans carry no cluster identity, so
  correlation resolves within the local cluster's resources.

## Metrics

| Metric | Meaning |
| --- | --- |
| `kubeatlas_otel_received_total` | Spans received over OTLP gRPC. |
| `kubeatlas_otel_dropped_total` | Spans dropped because the queue was full. |
| `kubeatlas_otel_written_total` | Spans durably written to PostgreSQL. |
| `kubeatlas_otel_retention_deleted_total` | Spans deleted by the retention sweep. |
| `kubeatlas_otel_unmatched_spans_total` | Call endpoints the correlator could not map to a resource. |
| `kubeatlas_otel_runtime_edges_total` | `CALLS_AT_RUNTIME` overlay edges written. |

---
sidebar_position: 11
title: OpenTelemetry integration
---

# OpenTelemetry integration

This guide turns on the [OpenTelemetry runtime
overlay](../concepts/otel-overlay.md) and fans an existing trace
pipeline into KubeAtlas. The overlay lets KubeAtlas show observed
runtime calls (`CALLS_AT_RUNTIME`) layered over the declarative
dependency graph.

:::warning Tier 2 required
The overlay stores spans in PostgreSQL. It is available **only** on a
Tier 2 install (`store.backend=postgres`). On Tier 1, or with
`otel.enabled=false`, the `/api/v1/otel/*` endpoints return `503`. See
[Persistence](./persistence.md) to enable Tier 2 first.
:::

## 1. Enable the receiver

```yaml
# values.yaml
store:
  backend: postgres          # Tier 2 is required for the overlay

otel:
  enabled: true              # default false — turns on the receiver + correlator
  receiver:
    port: 4317               # OTLP/gRPC listen port (default 4317)
    bufferSize: 4096         # span-queue capacity; a full queue drops (default 4096)
  retention: 7d              # how long spans + runtime edges are kept; day suffix or Go duration (default 7d)
```

```bash
helm upgrade --install kubeatlas \
  oci://ghcr.io/lithastra/charts/kubeatlas --version 1.5.0 \
  -f values.yaml
```

With `otel.enabled=true`, the chart renders a Service exposing the OTLP
gRPC port `4317` alongside the existing `8080` HTTP API. The receiver is
a **no-op** when disabled — no port is opened and no goroutine spawned —
so leaving it off costs nothing.

## 2. Point a Collector at KubeAtlas

KubeAtlas speaks raw OTLP/gRPC. If you already run an OpenTelemetry
Collector, add KubeAtlas as an **additional exporter** so your existing
trace backend (Jaeger, Tempo, …) keeps receiving everything — KubeAtlas
is a fan-out, not a replacement.

```yaml
# otel-collector-config.yaml
receivers:
  otlp:
    protocols:
      grpc:
      http:

exporters:
  # your existing backend, unchanged
  otlp/tempo:
    endpoint: tempo.observability.svc:4317
    tls:
      insecure: true
  # fan a copy of the traces into KubeAtlas
  otlp/kubeatlas:
    endpoint: kubeatlas.kubeatlas.svc:4317
    tls:
      insecure: true

service:
  pipelines:
    traces:
      receivers: [otlp]
      # both exporters receive every span
      exporters: [otlp/tempo, otlp/kubeatlas]
```

Sending directly from an SDK exporter works too — just set its OTLP
endpoint to `kubeatlas.<namespace>.svc:4317`.

## 3. Make sure the K8s attributes are present

The correlator maps spans to graph resources using the standard
Kubernetes OpenTelemetry **resource attributes**. Set them on your
workloads' spans — most commonly via the Collector's `k8sattributes`
processor:

```yaml
processors:
  k8sattributes:
    extract:
      metadata:
        - k8s.namespace.name
        - k8s.deployment.name
        - k8s.pod.name

service:
  pipelines:
    traces:
      processors: [k8sattributes]
```

| Attribute | Used for |
| --- | --- |
| `service.name` | Distinguishing caller from callee. **Required** for an edge to be inferred. |
| `k8s.namespace.name` | Scoping the resource lookup. |
| `k8s.deployment.name` | Preferred resolution target (workload identity). |
| `k8s.pod.name` | Fallback when no deployment attribute is present. |

Spans missing these degrade gracefully — they are counted in
`kubeatlas_otel_unmatched_spans_total` and skipped, never fatal.

## 4. Verify

```bash
# a namespaced network policy should let 4317 through from your Collector
kubectl -n kubeatlas port-forward svc/kubeatlas 8080:8080 &

# after traffic has flowed for a minute, the overlay should return edges
curl -s 'http://localhost:8080/api/v1/otel/overlay?namespace=petclinic' | jq '.count'
# > 0

# the compare view classifies declared vs observed
curl -s 'http://localhost:8080/api/v1/otel/overlay?namespace=petclinic&compare=true' | jq '.summary'
```

Then open the Web UI, navigate to the topology graph at **namespace**
level, and flip the **OTel overlay** toggle — observed runtime calls
appear as animated blue dotted edges.

## Tuning

| Value | Default | Notes |
| --- | --- | --- |
| `otel.enabled` | `false` | Master switch. |
| `otel.receiver.port` | `4317` | OTLP/gRPC listen port. |
| `otel.receiver.bufferSize` | `4096` | Span-queue capacity; a full queue drops. |
| `otel.retention` | `7d` | Span + runtime-edge retention (day suffix or Go duration). |

Environment equivalents (set by the chart): `KUBEATLAS_OTEL_ENABLED`,
`KUBEATLAS_OTEL_GRPC_ADDR`, `KUBEATLAS_OTEL_BUFFER_SIZE`,
`KUBEATLAS_OTEL_RETENTION`.

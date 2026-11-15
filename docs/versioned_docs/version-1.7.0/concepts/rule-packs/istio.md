---
sidebar_position: 2
title: Istio
---

# Istio rule pack

The `istio` pack models the traffic-routing relationships Istio's
`networking.istio.io` API group adds to a cluster.

- **OCI artifact:** `oci://ghcr.io/lithastra/rules/istio:0.1.0`
- **Requires:** KubeAtlas ≥ 1.1.0

## Modules

| Module | Matched kind | Edges |
|--------|--------------|-------|
| `virtualservice` | `VirtualService` | `ROUTES_TO`, `BINDS_GATEWAY` |
| `destinationrule` | `DestinationRule` | `CONFIGURES` |
| `gateway` | `Gateway` | `USES_TLS_SECRET` |

## Edges

- **`ROUTES_TO`** — a VirtualService to each Service named by a
  `spec.http`, `spec.tcp` or `spec.tls` route destination.
- **`BINDS_GATEWAY`** — a VirtualService to each Gateway in
  `spec.gateways[]`. The reserved value `mesh` (the sidecar mesh)
  is skipped.
- **`CONFIGURES`** — a DestinationRule to the Service named by
  `spec.host`.
- **`USES_TLS_SECRET`** — a Gateway to the Kubernetes Secret named
  by a server's `tls.credentialName`.

## Host resolution

VirtualService and DestinationRule reference Services by host
string. The pack follows Istio's own rule:

- a host with **no dots** is a Service in the resource's namespace;
- a host containing **`.svc`** is a cluster FQDN
  (`<name>.<namespace>.svc[.cluster.local]`);
- anything else is an **external host** — reached through a
  ServiceEntry — and derives no edge.

## Out of scope

ServiceEntry, Sidecar, EnvoyFilter and WorkloadEntry; the Gateway
API (`gateway.networking.k8s.io`); Gateway `spec.selector` label
matching; Istio's security and telemetry CRDs.

## Try it

```bash
kubeatlas rules-test --pack=oci://ghcr.io/lithastra/rules/istio:0.1.0
```

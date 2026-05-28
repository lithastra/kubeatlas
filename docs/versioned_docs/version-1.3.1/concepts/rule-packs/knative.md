---
sidebar_position: 4
title: Knative
---

# Knative rule pack

The `knative` pack models the Service → Configuration → Revision
generation chain the `serving.knative.io` API group adds.

- **OCI artifact:** `oci://ghcr.io/lithastra/rules/knative:0.1.0`
- **Requires:** KubeAtlas ≥ 1.1.0

## Modules

One module, registered for two kinds:

| Module | Matched kind | Edges |
|--------|--------------|-------|
| `service` | `Service` | `CREATES_CONFIGURATION` |
| `service` | `Configuration` | `CREATES_REVISION` |

## Edges

- **`CREATES_CONFIGURATION`** — a Knative Service to the same-named
  Configuration Knative provisions for it.
- **`CREATES_REVISION`** — a Configuration to the Revisions named
  by `status.latestCreatedRevisionName` and
  `status.latestReadyRevisionName`.

Kubernetes ownerReferences already give the generic `OWNS` edges
down the same chain; these Knative-typed edges name it.

## Out of scope

Route and DomainMapping traffic targets (the percentage split
across many Revisions); Knative Eventing CRDs (Source, Trigger,
Broker, Channel).

## Try it

```bash
kubeatlas rules-test --pack=oci://ghcr.io/lithastra/rules/knative:0.1.0
```

---
sidebar_position: 1
title: Helm install options
---

# Helm install options

The chart is published as an OCI artifact:

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.5.2 \
  --namespace kubeatlas --create-namespace
```

This page documents every value the chart honours. Defaults err on
the side of "secure and unexposed" — see
[secure defaults](#secure-defaults-summary) below.

## Reference

Pick the most useful subset for your situation; the rest take their
defaults.

### `image`

| Key | Default | Notes |
|---|---|---|
| `image.repository` | `ghcr.io/lithastra/kubeatlas` | Set this to point at a mirror or private registry. |
| `image.tag` | `""` (uses `Chart.AppVersion`) | Pin to an immutable digest in production. |
| `image.pullPolicy` | `IfNotPresent` | |
| `imagePullSecrets` | `[]` | Reference Secrets that already exist in the namespace. |

### `service`

| Key | Default | Notes |
|---|---|---|
| `service.type` | `ClusterIP` | The schema rejects `NodePort` and `LoadBalancer` on purpose; expose via Ingress + auth. |
| `service.port` | `80` | |
| `service.containerPort` | `8080` | Matches the binary's listen address. |

### `ingress`

| Key | Default | Notes |
|---|---|---|
| `ingress.enabled` | `false` | See [security warning](./security-warning.md) before flipping this. |
| `ingress.acknowledgeNoBuiltinAuth` | `false` | Must be `true` to enable the Ingress — the schema enforces it via `if/then`. |
| `ingress.className` | `""` | Maps to a controller installed in the cluster (e.g. `nginx`, `traefik`, `alb`). |
| `ingress.annotations` | `{}` | Controller-specific config. |
| `ingress.hosts` | one example host | Standard `host` + `paths[].path` + `pathType` shape. |
| `ingress.tls` | `[]` | TLS secret references. cert-manager integration is on the v1.0 [roadmap](../roadmap.md). |

Per-controller example values:

- [F5 NGINX](./ingress-nginx-f5.md)
- [Traefik](./ingress-traefik.md)
- [AWS ALB](./ingress-alb.md)

### `rbac` and `serviceAccount`

| Key | Default | Notes |
|---|---|---|
| `rbac.create` | `true` | Disables only if you're managing the ClusterRole/ClusterRoleBinding out-of-band. |
| `serviceAccount.create` | `true` | |
| `serviceAccount.name` | `""` | Empty → derived from the release name. |
| `serviceAccount.annotations` | `{}` | For IRSA / Workload Identity. |

The ClusterRole's verbs are **hard-coded** to `[get, list, watch]`, and its API
groups/resources are explicitly enumerated. It does not grant any permission
on core/v1 Secrets and does not use a wildcard across API groups. Kubernetes
RBAC cannot return Secret metadata without returning the complete object, so
granting Secret access out of band weakens KubeAtlas's supported security
boundary.

The chart enumerates the CRD API groups used by its built-in integrations:
cert-manager Certificates/issuers and ACME requests, CloudNativePG Tier 2
resources, Kyverno policies/reports, and Gatekeeper templates/constraints.
These grants expose the non-Secret custom resources to the graph but never the
Kubernetes Secrets that cert-manager or CloudNativePG creates or references.
Another rule pack may require an operator-managed read-only ClusterRole for its
exact non-core API group and resources; do not solve that requirement with
`apiGroups: ["*"]` and `resources: ["*"]`.

### Security context

Defaults are tightened — the schema refuses to relax them below the
spec's bar (`runAsNonRoot=true`, `readOnlyRootFilesystem=true`,
`drop: [ALL]`). You can change UIDs / GIDs, but you cannot toggle
the hardening off.

### `resources`

| Key | Default |
|---|---|
| `resources.requests.cpu` | `100m` |
| `resources.requests.memory` | `128Mi` |
| `resources.limits.cpu` | `500m` |
| `resources.limits.memory` | `512Mi` |

A 1000-resource cluster steady-states at ~110 MB; raise the memory
limit if your fleet is significantly larger.

### `networkPolicy`

| Key | Default | Notes |
|---|---|---|
| `networkPolicy.enabled` | `true` | Default-deny inbound to the KubeAtlas Pod. |
| `networkPolicy.ingressNamespaceLabel` | `""` | Namespace label that allows traffic in. Disable the whole thing for ALB / external load balancers — see the [ALB example](./ingress-alb.md). |

### `persistence`

| Key | Default | Notes |
|---|---|---|
| `persistence.enabled` | `false` | Tier 1 is in-memory only. Set `true` for PostgreSQL + Apache AGE Tier 2. |
| `persistence.embedded.enabled` | `false` | Create a CNPG-managed PostgreSQL `Cluster`; current main requires the external CloudNativePG chart 0.29.0 / operator 1.30.0 first. |
| `persistence.embedded.retainOnDelete` | `true` | Keep the CNPG `Cluster` and PVC on KubeAtlas uninstall. |
| `persistence.embedded.storageSize` | `5Gi` | Requested PVC size; cannot be shrunk in place. |

See [Persistence (Tier 2)](./persistence.md) for the prerequisite,
v1.5.0 upgrade, failure-recovery, and deletion procedures.

### Probes and scheduling

`livenessProbe` and `readinessProbe` map to `/healthz` and `/readyz`.
`/readyz` only flips ready after the informer's initial sync, so a
green readiness gate means the graph is fully populated.

`nodeSelector`, `tolerations`, and `affinity` follow the standard
Helm chart shape.

## Secure defaults summary

Five things are pinned together by `values.schema.json` so flipping
any one in isolation either fails the schema or silently has no
effect:

1. `service.type` is restricted to `ClusterIP`.
2. `ingress.enabled=true` requires `ingress.acknowledgeNoBuiltinAuth=true`.
3. ClusterRole verbs are template-fixed at `[get, list, watch]`.
4. Pod and container `securityContext` defaults are non-root +
   read-only root + dropped capabilities.
5. The chart never installs a database; persistence is disabled.

Operators who need to weaken any of these have to touch multiple
values. That friction is intentional — see §2.3 in the spec
for the rationale.

## Uninstall

```bash
helm uninstall kubeatlas -n kubeatlas
```

For Tier 1, the in-memory graph disappears with the Pod. For
embedded Tier 2, the database and PVC are retained by default. Do
not delete the namespace until you have followed the explicit
[Tier 2 data-deletion procedure](./persistence.md#uninstall-and-data-retention).

---
sidebar_position: 1
title: Helm install options
---

# Helm install options

The chart is published as an OCI artifact:

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 0.1.0 \
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

The ClusterRole's verbs are **hard-coded** to `[get, list, watch]`
inside the template. There is no values toggle: the read-only
invariant is a product promise, not a knob.

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
| `persistence.enabled` | `false` | Tier 1 is in-memory only. PostgreSQL + Apache AGE is Tier 2 / v1.0 — see [roadmap](../roadmap.md). |

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
values. That friction is intentional — see Phase 1 §2.3 in the spec
for the rationale.

## Uninstall

```bash
helm uninstall kubeatlas -n kubeatlas
kubectl delete namespace kubeatlas
```

The in-memory graph disappears with the Pod; nothing persists.

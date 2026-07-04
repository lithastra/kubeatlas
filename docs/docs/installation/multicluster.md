---
sidebar_position: 10
title: Multi-cluster & RBAC visibility
---

# Multi-cluster federation

KubeAtlas can attach to several clusters at once and serve a **federated
graph** across them. Every resource is tagged with its `ClusterID`, so
the UI can group and colour by cluster and the API can return a merged
or per-cluster view.

## Enable federation

Federation reads a Secret whose **keys are cluster names** and whose
**values are kubeconfigs**:

```bash
kubectl -n kubeatlas create secret generic member-kubeconfigs \
  --from-file=prod=./prod.kubeconfig \
  --from-file=staging=./staging.kubeconfig
```

```yaml
# values.yaml
multicluster:
  enabled: true
  kubeconfigSecret: member-kubeconfigs
```

Each key becomes a `ClusterID`; it must not contain `:` or `/` (those
are reserved in the resource-ID prefix). Attachment is best-effort — a
single bad kubeconfig is logged and skipped, the rest still attach.

The federated surface is served under `/api/v1/federation/*`:

| Endpoint | Returns |
| --- | --- |
| `GET /api/v1/federation/clusters` | The attached member clusters. |
| `GET /api/v1/federation/graph?cluster=a,b` | A merged (or per-cluster, `level=cluster`) view across the named clusters. |

## RBAC visibility (F-206)

By default every caller can see every attached cluster. When you run one
KubeAtlas in front of clusters owned by different teams, you can restrict
**which clusters each caller sees** with a set of token rules.

:::info Read-side visibility only
This is a deliberately small, simplified multi-tenancy control. It
filters *which clusters a request may see* on the federation surface —
nothing more. It does **not** fetch or rotate cluster credentials, add
OIDC/SSO, or introduce any CRD. Deeper tenancy and credential lifecycle
are future decisions, not part of v1.5.
:::

Each rule maps a **bearer token** to a list of clusters:

```yaml
# values.yaml
multicluster:
  enabled: true
  kubeconfigSecret: member-kubeconfigs
  rbac:
    rules:
      - token: "REPLACE-team-a-token"
        clusters: [prod, prod-dr]
      - token: "REPLACE-team-b-token"
        clusters: [staging]
```

A caller then presents its token as a standard bearer header:

```bash
curl -H "Authorization: Bearer REPLACE-team-a-token" \
  http://kubeatlas:8080/api/v1/federation/clusters
# -> { "mode": "federated", "clusters": ["prod", "prod-dr"] }   (staging is hidden)
```

Behaviour:

- **No rules configured** → the federation surface is open to everyone,
  exactly as v1.4. Fully backward compatible.
- **Matched token** → the caller sees only that rule's clusters.
  `/federation/clusters` lists just those; `/federation/graph` serves
  them and **rejects** any other requested cluster with `403`.
- **No token, rules configured** → `401 Unauthorized`.
- **Unknown token** → `403 Forbidden`.

An unauthorised caller is always *rejected*, never handed a silently
empty list — so it can tell "denied" from "no clusters attached".

:::warning Handle tokens as secrets
Tokens are secret material. Supply them from a secret values source
(sealed-secrets, SOPS, `helm upgrade -f secret-values.yaml`) — never
commit plaintext tokens to a values file checked into git. The chart
renders them into a `<release>-rbac-rules` Secret mounted read-only into
the pod, and the process keeps only their SHA-256 hashes in memory.
:::

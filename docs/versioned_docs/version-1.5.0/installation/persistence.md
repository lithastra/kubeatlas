---
sidebar_position: 5
title: Persistence (Tier 2)
---

# Persistence (Tier 2)

KubeAtlas v1.0 ships with two storage tiers:

| Tier | Backend | Default? | Restart safe? | Use when |
|---|---|---|---|---|
| Tier 1 | In-memory | Yes | No | Evaluating, dev clusters, "I just want to look at the graph" |
| Tier 2 | PostgreSQL + Apache AGE | No | Yes | Production, multi-replica, surviving Pod restarts |

A bare `helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas` keeps you on Tier 1. Tier 2 is opt-in via `--set persistence.enabled=true` plus exactly one of `embedded` or `connection`. The schema rejects half-configured installs at `helm install` time so you cannot accidentally end up with Tier 2 enabled and no database to talk to.

## Decision tree

```
                persistence.enabled?
                       │
            ┌──────────┴──────────┐
            no                    yes
            │                     │
        Tier 1 (default)      embedded.enabled?
                                  │
                       ┌──────────┴──────────┐
                       yes                   no
                       │                     │
              CNPG sub-chart           BYO Postgres
              one-line install        (connection.host …)
```

## Path A: Embedded CloudNativePG (one-line install)

The chart ships an optional dependency on [CloudNativePG](https://cloudnative-pg.io/) that is only fetched when `persistence.embedded.enabled=true`. The cnpg operator provisions a single-replica `Cluster` that runs `lithastra/postgres-age:16-1.6.0` (PostgreSQL 16 + Apache AGE 1.6.0).

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --namespace kubeatlas --create-namespace \
  --set persistence.enabled=true \
  --set persistence.embedded.enabled=true
```

What this does:

1. Installs the cnpg operator into your cluster.
2. Renders a `Cluster` CRD called `<release>-pg`. The operator reconciles it into a StatefulSet, PVC, Services, and a `<release>-pg-app` Secret.
3. Configures the cluster with `shared_preload_libraries=age` and a `postInitApplicationSQL` step that runs `CREATE EXTENSION IF NOT EXISTS age` against the bootstrapped database.
4. Wires the kubeatlas Pod to point at the `<release>-pg-rw` Service. The Pod's `wait-for-pg` init container blocks startup until `pg_isready` succeeds, so the main container never sees a half-up Postgres.

### Tunable values

| Value | Default | Notes |
|---|---|---|
| `persistence.embedded.image` | `ghcr.io/lithastra/postgres-age:16-1.6.0` | Multi-arch image (amd64 + arm64). Pin to a specific tag in production; never use `:latest`. |
| `persistence.embedded.storageSize` | `5Gi` | PVC size. CNPG cannot shrink this in place; size for projected graph growth. |
| `persistence.embedded.storageClassName` | _(empty → cluster default)_ | Set to a fast SSD class for production. |
| `persistence.embedded.clusterNameSuffix` | `pg` | Final cluster name is `<release>-<suffix>`. |

## Path B: BYO Postgres + AGE

For shops that already run a managed PG (with AGE installed) or want fine-grained ops, point KubeAtlas at an existing instance:

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --namespace kubeatlas --create-namespace \
  --set persistence.enabled=true \
  --set persistence.connection.host=postgres.example.com \
  --set persistence.connection.user=kubeatlas \
  --set persistence.connection.passwordSecretRef.name=kubeatlas-pg-creds \
  --set persistence.connection.passwordSecretRef.key=password
```

`passwordSecretRef` is the production-recommended path — the rendered Deployment never carries the password as a literal, only the Secret reference. The plaintext `connection.password` field is also accepted but only fits dev / disposable clusters.

### Compatibility matrix

| Provider | AGE-capable? | Notes |
|---|---|---|
| Self-hosted PostgreSQL | ✅ | Install `apache/age` extension; set `shared_preload_libraries=age`. |
| CloudNativePG | ✅ | What "Path A" above provisions. |
| Azure Database for PostgreSQL — Flexible Server | ✅ (with extension allowlist) | Add `age` to the `azure.extensions` parameter; AGE 1.5+ supported on PG 14+. |
| Crunchy Postgres for Kubernetes | ✅ | Mount the AGE shared library; same `shared_preload_libraries` config. |
| AWS RDS for PostgreSQL | ❌ | Does not allow non-allowlisted extensions; `shared_preload_libraries=age` is rejected. |
| Google Cloud SQL for PostgreSQL | ❌ | Same restriction as RDS. |
| Aurora PostgreSQL | ❌ | Same restriction as RDS. |

> **If your provider is not on this list:** the gating question is whether they let you set `shared_preload_libraries=age` and install the AGE extension. If yes, KubeAtlas will work. If no, switch to embedded (Path A) or self-host PG.

## Verification

Once the Pod is `Ready`, check that AGE is reachable:

```bash
kubectl exec -n kubeatlas deploy/kubeatlas -- \
  curl -s localhost:8080/healthz
# {"status":"ok","backend":"postgres","schemaVersion":1}
```

The `/healthz` schema-version field surfaces the migration version the binary applied; if it is 0, the migration framework rolled back and the Pod will not become ready (init container's `wait-for-pg` and the main container's startup probe both gate on this).

## Restarting

Tier 2 survives Pod restarts. The graph reloads from PostgreSQL on next start, so you should observe:

- Cluster-level view populated within a few seconds (no informer re-scan needed for cached resources).
- A short re-sync window where the informer reconciles any changes that happened during the restart, then the Pod marks itself ready.

## Mutual exclusion

The schema enforces:

- `persistence.enabled=true` AND neither `embedded.enabled=true` nor `connection.host` set → install rejected.
- `persistence.enabled=true` AND BOTH `embedded.enabled=true` AND `connection.host` set → install rejected (ambiguous wiring).

This is intentional: a half-configured persistence setup that "almost works" is worse than a clear failure at install time.

---
sidebar_position: 5
title: Persistence (Tier 2)
---

# Persistence (Tier 2)

KubeAtlas v1.0 ships with two storage tiers:

| Tier | Backend | Default? | Restart safe? | Use when |
|---|---|---|---|---|
| Tier 1 | In-memory | Yes | No | Evaluating, dev clusters, "I just want to look at the graph" |
| Tier 2 | PostgreSQL + Apache AGE | No | Yes | Persistent single-replica deployment, surviving Pod restarts |

A bare `helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas` keeps you on Tier 1. Tier 2 is opt-in via `--set persistence.enabled=true` plus exactly one of `embedded` or `connection`. The schema rejects half-configured installs at `helm install` time so you cannot accidentally end up with Tier 2 enabled and no database to talk to.

KubeAtlas currently supports one application replica. The chart rejects
`replicaCount>1`: without leader election, multiple pods would duplicate
informer events and migration work. Tier 2 upgrades use `Recreate`, so expect a
brief application outage while the old pod stops, migrations run, and the new
informer completes its initial sync. PostgreSQL itself may use a separate
high-availability topology, but that does not make the KubeAtlas API
zero-downtime.

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
          CNPG-managed Cluster         BYO Postgres
          (operator installed first)  (connection.host …)
```

## Path A: Embedded CloudNativePG

Install the [CloudNativePG](https://cloudnative-pg.io/) 0.22.1
operator once per cluster, then enable embedded persistence in the
KubeAtlas chart. Keeping the cluster-scoped operator in its own Helm
release prevents one KubeAtlas uninstall from removing control-plane
resources shared by other databases.

```bash
helm repo add cloudnative-pg https://cloudnative-pg.io/charts
helm repo update
helm upgrade --install cnpg cloudnative-pg/cloudnative-pg \
  --version 0.22.1 \
  --namespace cnpg-system --create-namespace \
  --wait --timeout 5m

kubectl wait --for=condition=Established \
  crd/clusters.postgresql.cnpg.io \
  --timeout=2m

helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.5.1 \
  --namespace kubeatlas --create-namespace \
  --set persistence.enabled=true \
  --set persistence.embedded.enabled=true
```

What this does:

1. Installs one cluster-scoped CNPG operator in `cnpg-system`.
2. KubeAtlas renders a namespaced `Cluster` custom resource called
   `<release>-pg`. The operator reconciles it into a PostgreSQL Pod,
   PVC, Services, and a `<release>-pg-app` Secret.
3. The cluster uses `ghcr.io/lithastra/postgres-age:16.6-age1.6.0-rc0.1`
   (PostgreSQL 16 + Apache AGE 1.6.0), loads AGE at server start, and
   runs `CREATE EXTENSION IF NOT EXISTS age` during bootstrap.
4. The KubeAtlas Pod points at the `<release>-pg-rw` Service. Its
   `wait-for-pg` init container blocks startup until `pg_isready`
   succeeds.

### Tunable values

| Value | Default | Notes |
|---|---|---|
| `persistence.embedded.image` | `ghcr.io/lithastra/postgres-age:16.6-age1.6.0-rc0.1` | Multi-arch image (amd64 + arm64). The tag records PostgreSQL 16.6, the only upstream PG16 AGE 1.6.0 candidate, and KubeAtlas image revision 1; never use `:latest`. |
| `persistence.embedded.storageSize` | `5Gi` | PVC size. CNPG cannot shrink this in place; size for projected graph growth. |
| `persistence.embedded.storageClassName` | _(empty → cluster default)_ | Set to a fast SSD class for production. |
| `persistence.embedded.clusterNameSuffix` | `pg` | Final cluster name is `<release>-<suffix>`. |
| `persistence.embedded.retainOnDelete` | `true` | Keep the CNPG `Cluster` and PVC when the KubeAtlas Helm release is uninstalled. |

### Upgrade from v1.5.0

v1.5.0 bundled the operator inside the KubeAtlas release. Install
the external operator first and let Helm transfer ownership of the
CNPG CRDs, then upgrade KubeAtlas:

```bash
# Helm 3.20+ is required for --take-ownership.
helm repo add cloudnative-pg https://cloudnative-pg.io/charts
helm repo update
helm upgrade --install cnpg cloudnative-pg/cloudnative-pg \
  --version 0.22.1 \
  --namespace cnpg-system --create-namespace \
  --take-ownership \
  --wait --timeout 5m

helm upgrade kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.5.1 \
  --namespace kubeatlas \
  --reuse-values \
  --set persistence.embedded.retainOnDelete=true \
  --wait --timeout 8m
```

The database Pod and PVC stay in place during this transition. After
the upgrade, `kubeatlas-cloudnative-pg` in the KubeAtlas namespace
must be gone and `cnpg-cloudnative-pg` in `cnpg-system` must be
Ready.

### Security upgrade to v1.5.2

Tier 2 schema v11 permanently removes previously stored Kubernetes Secret
payloads, replaces Secret rows with reference-only placeholders, clears all
snapshot payloads, and removes stale graph edges incident to Secrets. The
application remains unready until this transaction succeeds; the initial
informer sync then recreates current incoming Secret-reference edges.

Stop treating a database migrated to schema v11 as compatible with v1.5.1:
older binaries fail closed on the newer schema and rollback is unsupported.
Take a recovery backup before the upgrade, protect it as sensitive data, and
delete it according to your retention policy after the recovery window. The
migration cannot scrub external backups, snapshots, replicas, exports, or log
archives that were created before v1.5.2.

The first upgrade from v1.5.1 (or older) changes the application Deployment
from Kubernetes' default rolling strategy to `Recreate`. Helm 3 applies the
required field removal with its normal client-side patch. Helm 4 defaults to
server-side apply, which cannot atomically remove the old defaulted
`rollingUpdate` field while changing the strategy type. On Helm 4, run this
first security upgrade with `--server-side=false`:

```bash
helm upgrade kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.5.2 \
  --namespace kubeatlas \
  --reuse-values \
  --server-side=false \
  --wait --timeout 8m
```

This flag changes only how Helm patches the manifests; it does not weaken the
KubeAtlas runtime security boundary. Subsequent upgrades start from a
`Recreate` Deployment and do not need this one-time transition workaround.

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

## Uninstall and data retention

With the default `persistence.embedded.retainOnDelete=true`,
uninstalling KubeAtlas removes the application but leaves the CNPG
`Cluster` and PVC:

```bash
helm uninstall kubeatlas -n kubeatlas
kubectl get cluster.postgresql.cnpg.io,pvc -n kubeatlas
```

Do not delete the namespace if you intend to retain that data. When
permanent deletion is intentional, remove the CNPG `Cluster`
explicitly and wait for its PVC cleanup:

```bash
# Destructive: permanently deletes the embedded database.
kubectl delete cluster.postgresql.cnpg.io kubeatlas-pg -n kubeatlas
```

The `cnpg` operator release is cluster-scoped infrastructure. Remove
it only after confirming no other CNPG clusters depend on it.

## Mutual exclusion

The schema enforces:

- `persistence.enabled=true` AND neither `embedded.enabled=true` nor `connection.host` set → install rejected.
- `persistence.enabled=true` AND BOTH `embedded.enabled=true` AND `connection.host` set → install rejected (ambiguous wiring).

This is intentional: a half-configured persistence setup that "almost works" is worse than a clear failure at install time.

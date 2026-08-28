---
sidebar_position: 1
title: Signals, alerts, and recovery
---

# Signals, alerts, and recovery

KubeAtlas v1.6 exposes product-neutral Prometheus text metrics at `/metrics`.
The chart does not install Prometheus, a `ServiceMonitor` or `PodMonitor` CRD,
Grafana dashboards, or a vendor-specific monitoring agent. Connect the existing
monitoring system used by your DevOps team to the ClusterIP Service and own its
retention, routing, and escalation policy there.

:::warning Protect the monitoring and application surfaces

KubeAtlas has no built-in authentication. Keep the Service cluster-internal or
put every exposed route, including `/metrics`, behind external authentication
and authorization. KubeAtlas intentionally stores and displays broad cluster
information: ConfigMap values, workload specifications, RBAC names,
annotations, and topology can all be sensitive. Only Kubernetes Secret values
and runtime credentials are excluded. An unauthenticated KubeAtlas endpoint is
therefore still a cluster-wide information leak. Read
[Authentication is your job](../installation/security-warning.md) before
exposing it.

:::

## What the signals mean

| Metric | Meaning |
|---|---|
| `kubeatlas_go_memory_limit_bytes` | Go's current soft runtime-managed-memory boundary. The chart derives it from the container limit; compare it with `resources.limits.memory` when investigating memory pressure. |
| `kubeatlas_informer_synced` | `1` after the initial informer cache sync. It does not return to `0` after a later API outage, so do not use it alone as a freshness signal. |
| `kubeatlas_graph_observation_state{state="..."}` | One-hot graph state: `initializing`, `synced`, `degraded`, or `stale`. Exactly one series is `1`. |
| `kubeatlas_kubernetes_api_reachable` | Result of the latest bounded, read-only Kubernetes API probe. In multi-cluster mode it is `1` only when every attached member responds. |
| `kubeatlas_kubernetes_api_last_success_timestamp_seconds` | Last successful API probe, or `0` before the first success. |
| `kubeatlas_storage_reachable` | Result of the latest bounded storage probe. Tier 2 probes PostgreSQL; Tier 1 memory is local to the process. |
| `kubeatlas_storage_durable` | `1` for PostgreSQL Tier 2 and `0` for in-memory Tier 1. Reachable memory is not durable storage. |
| `kubeatlas_storage_last_success_timestamp_seconds` | Last successful storage probe, or `0` before the first success. |
| `kubeatlas_snapshot_queue_drop_total` | Snapshot events dropped because the asynchronous queue was full. Present only when snapshots are enabled. |
| `kubeatlas_snapshot_write_failed_total` | Snapshot events abandoned after the retry budget. Present only when snapshots are enabled. |
| `kubeatlas_otel_dropped_total` | Spans dropped because the opt-in OTLP queue was full. Present only when OTel is enabled. |
| `kubeatlas_backup_status_available` | `1` when the optional operator-maintained backup timestamp marker is valid; `0` when it is disabled, missing, or invalid. |
| `kubeatlas_backup_last_success_timestamp_seconds` | Timestamp reported by that marker, or `0` when unavailable. |
| `kubeatlas_backup_age_seconds` | Age of the reported backup in seconds, or `0` when unavailable. Pair it with `kubeatlas_backup_status_available`; zero never means a fresh backup. |

The graph state is derived as follows:

- `initializing`: the informer has not completed initial sync, or the first API
  probe has not completed.
- `synced`: initial sync completed and the latest API probe succeeded.
- `degraded`: the latest API probe failed, but the most recent success is newer
  than `operations.staleAfter` (default two minutes).
- `stale`: the probe is still failing after that boundary, or no successful
  probe has ever occurred after initial sync.

A quiet cluster remains `synced`: freshness comes from a read-only API probe,
not from how recently a resource happened to change.

## Alert examples

These are plain PromQL examples, not installed alert rules. Adjust durations
and backup objectives to your operating policy.

```promql
# Graph data has crossed the configured stale boundary.
kubeatlas_graph_observation_state{state="stale"} == 1

# Durable Tier 2 storage is currently unreachable.
(kubeatlas_storage_durable == 1)
and
(kubeatlas_storage_reachable == 0)

# Snapshot work was dropped or permanently failed in the last five minutes.
increase(kubeatlas_snapshot_queue_drop_total[5m]) > 0
or
increase(kubeatlas_snapshot_write_failed_total[5m]) > 0

# Opt-in OTel work was dropped in the last five minutes.
increase(kubeatlas_otel_dropped_total[5m]) > 0

# No valid operator backup marker exists for a durable deployment.
(kubeatlas_storage_durable == 1)
and
(kubeatlas_backup_status_available == 0)

# The operator-reported backup is older than 24 hours.
(kubeatlas_backup_status_available == 1)
and
(kubeatlas_backup_age_seconds > 86400)
```

Suggested starting severities:

- page on sustained Tier 2 storage failure and a stale graph if operators rely
  on KubeAtlas during incidents;
- ticket on dropped snapshot/OTel work, then page if the counter continues to
  rise;
- set backup-marker and backup-age severity from the actual recovery point
  objective, not from the example 24-hour threshold.

Metrics contain fixed state labels and numeric values only. They do not contain
resource names, namespaces, probe errors, Secret values, database passwords,
kubeconfigs, tokens, or backup contents.

## Publish a backup timestamp marker

KubeAtlas does not take backups. Your backup job may update a small ConfigMap
only after the backup command and its integrity checks succeed:

```bash
kubectl create configmap kubeatlas-backup-status \
  --namespace kubeatlas \
  --from-literal=last-successful="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --dry-run=client -o yaml \
  | kubectl apply -f -

helm upgrade kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --namespace kubeatlas \
  --reuse-values \
  --set operations.backupStatus.configMapRef.name=kubeatlas-backup-status \
  --set operations.backupStatus.configMapRef.key=last-successful
```

The file accepts RFC 3339 or positive Unix seconds. The chart mounts only that
key, read-only. Put no backup payload, credentials, destination URL, object key,
or customer data in this ConfigMap. KubeAtlas reads at most 128 bytes and does
not log invalid contents or parsing errors.

This marker is an operator attestation, not independent proof that the archive
exists or can be restored. Update it only after the backup is complete, and
pair it with scheduled restore drills.

## Kubernetes API interruption

Observed signals:

1. `kubeatlas_kubernetes_api_reachable` becomes `0` after the next probe.
2. Graph state moves to `degraded`, then `stale` after `staleAfter`.
3. `kubeatlas_informer_synced` and `/readyz` remain successful because they
   record the completed initial sync; existing graph reads may be stale.

Operator response:

1. Confirm the control plane using your Kubernetes distribution's supported
   diagnostics. Do not restart KubeAtlas merely to clear the metric.
2. Keep the KubeAtlas endpoint protected while stale data is being served.
3. Restore API reachability and watch for
   `kubeatlas_kubernetes_api_reachable 1` plus graph state `synced`.
4. Escalate if both signals have not recovered within 120 seconds after the
   API server is responsive.

The disposable-kind drill is:

```bash
bash test/chaos/api-server-flap.sh
```

KubeAtlas must run outside the kind control-plane container so `/metrics`
remains reachable while the container is stopped.

## PostgreSQL interruption

Observed signals:

1. On Tier 2, `kubeatlas_storage_durable` remains `1` and
   `kubeatlas_storage_reachable` becomes `0`.
2. Graph reads that require PostgreSQL may fail; `/healthz` and `/readyz` do
   not claim database health.
3. Snapshot write failures can rise if the interruption exceeds the writer's
   retry budget. The queue may fill and drop events under sustained outage.

Operator response:

1. Inspect the CNPG `Cluster`, primary Pod, Services, and PVC. Do not delete a
   PVC as a reconnection test.
2. Restore a Ready primary using the database operator's supported procedure.
3. Require `kubeatlas_storage_reachable 1` and a successful graph read within
   120 seconds after the primary is Ready.
4. If dropped-work counters moved, record the observation gap and run the
   recovery procedure appropriate to your retention and backup policy.

The disposable embedded-Tier-2 drill is:

```bash
kubectl port-forward -n kubeatlas service/kubeatlas 18080:80
bash test/chaos/pg-disconnect.sh
```

See [Persistence](../installation/persistence.md) for the supported logical
backup and restore path. High-availability coordination remains outside the
v1.6 boundary; the application chart supports one KubeAtlas replica.

## Evidence gates

- Pull requests run the required frozen Kubernetes 1.34, 1.35, and 1.36 Tier 2
  matrix, plus required snapshot-overload evidence.
- A manual `Release preflight` composes artifact construction with that entire
  frozen candidate matrix. Every result must be definitive before release.
- `Weekly public clean cluster` anonymously resolves the current GitHub
  Release, verifies its checksum, pulls the matching public OCI Chart, checks
  versioned documentation, and installs both Tier 1 and Tier 2 on a fresh
  vanilla Kubernetes cluster.
- Failure and scheduled evidence contains bounded cluster state, logs, and
  metrics. A randomized raw/base64 Secret sentinel and the CNPG runtime
  password are scanned before retained evidence is accepted.

The exact automation inventory is maintained in
[the chaos README](https://github.com/lithastra/kubeatlas/blob/main/test/chaos/README.md).

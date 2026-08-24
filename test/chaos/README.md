# Chaos inventory

These scripts exercise failure and overload behavior against a disposable
Kubernetes environment. They are not all equivalent release gates. The table
below is the source of truth for what runs automatically, what is opt-in, and
what remains a manual operator drill.

Do not run them against a production cluster. Several scripts delete Pods,
disconnect a member cluster, stop a kind control-plane container, or create a
large burst of test resources.

## Automation status

| Script | Scenario | Automation status |
|---|---|---|
| `snapshot-write-storm.sh` | Saturate the snapshot writer and expose dropped work. | **Required PR CI** through `e2e-kind-snapshots.yml` and `phase3.sh` with `KUBEATLAS_RUN_CHAOS=1`. |
| `pg-disconnect.sh` | Delete the embedded CNPG primary; observe storage failure and recovery within 120 seconds. | **Opt-in suite** in `phase2.sh` when `KUBEATLAS_RUN_CHAOS=1`; also a manual production-readiness drill. |
| `rego-panic.sh` | Contain a panicking rule evaluation. | **Opt-in suite** in `phase2.sh` when `KUBEATLAS_RUN_CHAOS=1`. |
| `rego-runaway.sh` | Bound a non-terminating rule evaluation. | **Opt-in suite** in `phase2.sh` when `KUBEATLAS_RUN_CHAOS=1`. |
| `cert-manager-flap.sh` | Restart cert-manager and confirm certificate recovery. | **Opt-in suite** in `phase2.sh` when `KUBEATLAS_RUN_CHAOS=1`. |
| `otel-receiver-overload.sh` | Saturate the OTLP receiver and expose dropped spans. | **Opt-in heavy suite** in `phase5.sh` when `PHASE5_RUN_HEAVY=1`. |
| `api-server-flap.sh` | On Docker Desktop, withdraw only the kube-apiserver static-Pod manifest; observe degraded/stale graph state and recovery within 120 seconds. | **Manual** because it deliberately interrupts the active local API server. |
| `cluster-disconnect.sh` | Disconnect one in-cluster federation member. | **Manual** federation drill. |
| `cluster-disconnect-local.sh` | Disconnect one member from the local-binary federation fixture. | **Manual** federation drill. |
| `dangling-ref.sh` | Delete a referenced ConfigMap. | **Manual** graph-correctness drill. |
| `owner-loop.sh` | Create cyclic owner references. | **Manual** traversal-safety drill. |
| `resource-storm.sh` | Create 100 ConfigMaps quickly. | **Manual** informer/WebSocket throughput drill. |
| `telemetry-endpoint-down.sh` | Block the opt-in telemetry endpoint. | **Manual** telemetry-isolation drill. |

The weekly public clean-cluster check is not a chaos scenario. It runs from
`scheduled-clean-cluster.yml`, installs the current anonymous GitHub and Helm
OCI artifacts on vanilla Kubernetes, and retains sanitized logs and metrics.
The manual release preflight separately composes the frozen Kubernetes
candidate matrix from `e2e-kind-tier2.yml`.

## Common prerequisites

- `kubectl` pointed at a disposable cluster that matches the script header.
- `curl`, `jq`, and any scenario-specific tools listed by the script.
- KubeAtlas running with the feature under test enabled.
- A port-forward when the script expects a local metrics URL. Tier 2 scripts
  default to `127.0.0.1:18080`; `api-server-flap.sh` defaults to a local
  KubeAtlas process on port 8080.

Read each script header before running it. The federation, cert-manager, OTel,
snapshot, and Tier 2 scenarios require different fixtures; there is no single
cluster setup that honestly covers all of them.

## Required operational drills

For an API-server interruption:

```bash
# The exact context, kindest/node image, static manifest, and hold path are
# checked before mutation. An EXIT trap restores the manifest on failure.
KUBEATLAS_CONFIRM_API_FLAP=docker-desktop \
  bash test/chaos/api-server-flap.sh
```

The script probes the still-running KubeAtlas Pod from inside the Docker
Desktop node container while kubectl is unavailable. It requires
`kubeatlas_kubernetes_api_reachable` to become `0`, the
graph state to become `degraded` or `stale`, and both signals to recover within
120 seconds. `/readyz` remains the initial-sync gate and therefore stays `200`
after the first successful sync.

For a PostgreSQL interruption:

```bash
kubectl port-forward -n kubeatlas service/kubeatlas 18080:80
bash test/chaos/pg-disconnect.sh
```

The script requires `kubeatlas_storage_reachable` to become `0`, a replacement
CNPG primary to become Ready, storage reachability to return to `1`, and a graph
read to succeed within 120 seconds. It never deletes the database PVC.

## Reporting a divergence

Open an issue at <https://github.com/lithastra/kubeatlas/issues> with:

1. The exact script and KubeAtlas version.
2. Kubernetes distribution and server version.
3. Whether the run was required, opt-in, or manual.
4. Expected and observed metrics, including recovery time.
5. Sanitized logs. Never attach Secret values, database passwords,
   kubeconfigs, tokens, or full cluster dumps.

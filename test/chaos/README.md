# Chaos inventory

These scripts exercise failure and overload behavior against a disposable
Kubernetes environment. They are not all equivalent release gates. The table
below is the source of truth for what runs automatically, what is opt-in, and
what remains a manual operator drill.

Do not run them against a production cluster. Several scripts delete Pods,
disconnect a member cluster, interrupt PostgreSQL, or create a large burst
of test resources.

## Automation status

| Script | Scenario | Automation status |
|---|---|---|
| `snapshot-write-storm.sh` | Saturate the snapshot writer and expose dropped work. | **Required PR CI** through `e2e-kind-snapshots.yml` and `phase3.sh` with `KUBEATLAS_RUN_CHAOS=1`. |
| `pg-disconnect.sh` | Hibernate a single-instance CNPG cluster; observe storage failure, resume it, and verify application recovery within 120 seconds of the replacement primary becoming Ready. | **Opt-in suite** in `phase2.sh` when `KUBEATLAS_RUN_CHAOS=1`; also a manual production-readiness drill. |
| `rego-panic.sh` | Contain a panicking rule evaluation. | **Opt-in suite** in `phase2.sh` when `KUBEATLAS_RUN_CHAOS=1`. |
| `rego-runaway.sh` | Bound a non-terminating rule evaluation. | **Opt-in suite** in `phase2.sh` when `KUBEATLAS_RUN_CHAOS=1`. |
| `cert-manager-flap.sh` | Restart cert-manager and confirm certificate recovery. | **Opt-in suite** in `phase2.sh` when `KUBEATLAS_RUN_CHAOS=1`. |
| `otel-receiver-overload.sh` | Saturate the OTLP receiver and expose dropped spans. | **Opt-in heavy suite** in `phase5.sh` when `PHASE5_RUN_HEAVY=1`. |
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
  default to `127.0.0.1:18080`.

Read each script header before running it. The federation, cert-manager, OTel,
snapshot, and Tier 2 scenarios require different fixtures; there is no single
cluster setup that honestly covers all of them.

## Required operational drills

For an API-server interruption, use the Kubernetes distribution's supported
control-plane procedure in a disposable environment. Confirm the target
context and recovery procedure first, and keep the application's metrics
reachable independently of the interrupted API server. Platform-specific
control-plane orchestration is not included in these reusable scripts.

Require `kubeatlas_kubernetes_api_reachable` to become `0`, graph state to
become `degraded` or `stale`, and both signals to recover within 120 seconds
after API availability returns. `/readyz` remains the initial-sync gate and
therefore stays `200` after the first successful sync.

For a PostgreSQL interruption:

```bash
kubectl port-forward -n kubeatlas service/kubeatlas 18080:80
bash test/chaos/pg-disconnect.sh
```

The script requires a healthy single-instance CNPG cluster. It uses
[declarative hibernation](https://cloudnative-pg.io/docs/1.25/declarative_hibernation/)
to stop PostgreSQL while retaining its PVC, and waits for the configured
`stopDelay` plus 120 seconds (supporting `stopDelay` up to 3600 seconds).
Graceful shutdown can keep pooled connections alive for the 180-second smart
shutdown interval; the outage clock must not start at the deletion request.

Once hibernation is complete and no database Pods remain, the script requires
`kubeatlas_storage_reachable` to become `0` within 60 seconds while `/healthz`
continues to succeed. It then restores the original hibernation annotation,
waits up to 120 seconds for a Ready primary with a **different UID** (CNPG can
reuse the same Pod name), and requires storage reachability `1` and a successful
graph read within 120 seconds of that Ready observation. HTTP and Kubernetes
requests have timeouts. Application Pod replacement, container restarts, and
missing or changed panic counters fail the drill. Exit and signal handlers
attempt to undo hibernation even when a request fails; failed cleanup is
reported explicitly and requires operator action. This tests storage loss and
reconnection, not HA failover or crash recovery, and never deletes the PVC.

## Reporting a divergence

Open an issue at <https://github.com/lithastra/kubeatlas/issues> with:

1. The exact script and KubeAtlas version.
2. Kubernetes distribution and server version.
3. Whether the run was required, opt-in, or manual.
4. Expected and observed metrics, including recovery time.
5. Sanitized logs. Never attach Secret values, database passwords,
   kubeconfigs, tokens, or full cluster dumps.

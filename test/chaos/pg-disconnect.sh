#!/usr/bin/env bash
# test/chaos/pg-disconnect.sh - P2-T26 chaos scenario.
#
# Scenario: kill the embedded PG primary Pod (CNPG operator-
# managed) and verify KubeAtlas survives the disconnect, retries
# until PG is back, and recovers without panicking the process.
#
# Expected behaviour (v1.0):
#   - During the outage, KubeAtlas logs pgx connect errors but
#     does NOT exit. /healthz keeps returning 200; /readyz may
#     dip to 503 if Tier 2 reads gate readiness — that is fine.
#   - Within 30s of CNPG promoting a new primary, /readyz returns
#     200 again and the cluster-view endpoint is served.
#   - kubeatlas_rego_eval_panic_total has not increased.
#
# Skipped on Tier 1 installs (no PG to disconnect from). Use
# KUBEATLAS_TIER=tier1 to short-circuit explicitly.
#
# Anti-patterns guarded:
#   - The script does NOT delete the PG PVC — only the Pod.
#     Deleting the PVC would make CNPG provision a fresh empty
#     volume; we want to test reconnection, not replication.
#   - HA-aware: looks up the primary by CNPG label rather than
#     hardcoding pod-0, so future replica counts > 1 do not
#     break the script.

set -euo pipefail

NS="${NS:-kubeatlas}"
RELEASE="${RELEASE:-kubeatlas}"
TIER="${KUBEATLAS_TIER:-tier2}"
KUBEATLAS_PF_PORT="${KUBEATLAS_PF_PORT:-18080}"

if [[ "${TIER}" != "tier2" ]]; then
  echo "pg-disconnect: SKIPPED (KUBEATLAS_TIER=${TIER}; this scenario is Tier 2 only)"
  exit 0
fi

for cmd in kubectl curl jq; do
  command -v "${cmd}" >/dev/null || { echo "missing: ${cmd}" >&2; exit 1; }
done

echo "==> Locating CNPG primary Pod"
PRIMARY=$(kubectl get pod -n "${NS}" \
  -l "cnpg.io/cluster,role=primary" \
  -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)

if [[ -z "${PRIMARY}" ]]; then
  echo "pg-disconnect: no CNPG primary Pod found in ${NS} (Tier 2 not deployed?)"
  exit 1
fi
echo "Primary: ${PRIMARY}"

echo "==> Snapshot kubeatlas_rego_eval_panic_total before chaos"
metrics_before=$(curl -fsS "http://127.0.0.1:${KUBEATLAS_PF_PORT}/metrics" 2>/dev/null || echo "")
panic_before=$(grep '^kubeatlas_rego_eval_panic_total ' <<<"${metrics_before}" \
  | awk '{print $2}' | head -1)
panic_before=${panic_before:-0}
echo "panic_total before: ${panic_before}"

echo "==> Deleting primary Pod ${PRIMARY}"
kubectl delete pod -n "${NS}" "${PRIMARY}" --wait=false >/dev/null

echo "==> Sleeping 10s while CNPG promotes a replacement"
sleep 10

echo "==> Waiting up to 60s for a new primary to be Ready"
deadline=$((SECONDS + 60))
new_primary=""
while (( SECONDS < deadline )); do
  new_primary=$(kubectl get pod -n "${NS}" \
    -l "cnpg.io/cluster,role=primary" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [[ -n "${new_primary}" ]] && kubectl get pod -n "${NS}" "${new_primary}" \
       -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null \
       | grep -qw True; then
    break
  fi
  sleep 2
done
[[ -n "${new_primary}" ]] || { echo "no new primary surfaced within 60s"; exit 1; }
echo "New primary: ${new_primary}"

echo "==> Waiting up to 30s for kubeatlas /readyz to return 200"
deadline=$((SECONDS + 30))
ok=0
while (( SECONDS < deadline )); do
  if curl -fsS "http://127.0.0.1:${KUBEATLAS_PF_PORT}/readyz" >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 2
done
(( ok == 1 )) || { echo "kubeatlas /readyz did not recover within 30s"; exit 1; }
echo "kubeatlas /readyz: 200"

echo "==> Confirming panic counter did not increase"
metrics_after=$(curl -fsS "http://127.0.0.1:${KUBEATLAS_PF_PORT}/metrics")
panic_after=$(grep '^kubeatlas_rego_eval_panic_total ' <<<"${metrics_after}" \
  | awk '{print $2}' | head -1)
panic_after=${panic_after:-0}
echo "panic_total after: ${panic_after}"
if [[ "${panic_after}" != "${panic_before}" ]]; then
  echo "panic_total moved (${panic_before} -> ${panic_after}); chaos crossed a guard"
  exit 1
fi

echo
echo "pg-disconnect: kubeatlas survived PG primary loss, recovered within budget."

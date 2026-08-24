#!/usr/bin/env bash
# test/chaos/pg-disconnect.sh - P2-T26 chaos scenario.
#
# Scenario: kill the embedded PG primary Pod (CNPG operator-
# managed) and verify KubeAtlas survives the disconnect, retries
# until PG is back, and recovers without panicking the process.
#
# Expected behaviour (v1.6):
#   - During the outage, KubeAtlas logs pgx connect errors but
#     does NOT exit. /healthz keeps returning 200; /readyz may
#     dip to 503 if Tier 2 reads gate readiness — that is fine.
#   - kubeatlas_storage_reachable becomes 0 during the interruption.
#   - Within 120s of a replacement primary becoming Ready, storage
#     reachability returns to 1 and the cluster-view endpoint is served.
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
RESULT_FILE="${KUBEATLAS_CHAOS_RESULT_FILE:-}"

if [[ "${TIER}" != "tier2" ]]; then
  echo "pg-disconnect: SKIPPED (KUBEATLAS_TIER=${TIER}; this scenario is Tier 2 only)"
  exit 0
fi

for cmd in kubectl curl jq; do
  command -v "${cmd}" >/dev/null || { echo "missing: ${cmd}" >&2; exit 1; }
done
if [[ -n "${RESULT_FILE}" && -e "${RESULT_FILE}" ]]; then
  echo "refusing to overwrite existing result file ${RESULT_FILE}" >&2
  exit 1
fi

echo "==> Locating CNPG primary Pod"
PRIMARY=$(kubectl get pod -n "${NS}" \
  -l "cnpg.io/cluster=${RELEASE}-pg,cnpg.io/instanceRole=primary" \
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
grep -Fq 'kubeatlas_storage_reachable 1' <<<"${metrics_before}" || {
  echo "storage probe was not healthy before chaos" >&2
  exit 1
}

echo "==> Deleting primary Pod ${PRIMARY}"
kubectl delete pod -n "${NS}" "${PRIMARY}" --wait=false >/dev/null

echo "==> Waiting up to 60s for KubeAtlas to observe storage unreachability"
deadline=$((SECONDS + 60))
outage_observed=0
while (( SECONDS < deadline )); do
  metrics_during=$(curl -fsS "http://127.0.0.1:${KUBEATLAS_PF_PORT}/metrics" 2>/dev/null || true)
  if grep -Fq 'kubeatlas_storage_reachable 0' <<<"${metrics_during}"; then
    outage_observed=1
    break
  fi
  sleep 2
done
(( outage_observed == 1 )) || {
  echo "storage outage was not visible in metrics within 60s" >&2
  exit 1
}

echo "==> Waiting up to 120s for a replacement primary to be Ready"
deadline=$((SECONDS + 120))
new_primary=""
while (( SECONDS < deadline )); do
  new_primary=$(kubectl get pod -n "${NS}" \
    -l "cnpg.io/cluster=${RELEASE}-pg,cnpg.io/instanceRole=primary" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [[ -n "${new_primary}" && "${new_primary}" != "${PRIMARY}" ]] &&
     kubectl get pod -n "${NS}" "${new_primary}" \
       -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null \
       | grep -qw True; then
    break
  fi
  sleep 2
done
[[ -n "${new_primary}" && "${new_primary}" != "${PRIMARY}" ]] || {
  echo "no replacement primary surfaced within 120s"
  exit 1
}
echo "New primary: ${new_primary}"
recovery_started_at=$(date +%s)

echo "==> Waiting up to 120s for storage metrics and graph reads to recover"
deadline=$((SECONDS + 120))
ok=0
while (( SECONDS < deadline )); do
  metrics_after=$(curl -fsS "http://127.0.0.1:${KUBEATLAS_PF_PORT}/metrics" 2>/dev/null || true)
  if grep -Fq 'kubeatlas_storage_reachable 1' <<<"${metrics_after}" &&
     curl -fsS "http://127.0.0.1:${KUBEATLAS_PF_PORT}/api/v1/graph?level=cluster" >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 2
done
(( ok == 1 )) || { echo "KubeAtlas storage/graph did not recover within 120s"; exit 1; }
recovery_seconds=$(( $(date +%s) - recovery_started_at ))
(( recovery_seconds <= 120 )) || {
  echo "KubeAtlas recovered in ${recovery_seconds}s, exceeding the 120s budget" >&2
  exit 1
}
echo "kubeatlas storage and graph reads recovered"

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

if [[ -n "${RESULT_FILE}" ]]; then
  jq -n \
    --arg scenario postgresql-interruption \
    --arg original_primary "${PRIMARY}" \
    --arg replacement_primary "${new_primary}" \
    --argjson recovery_seconds "${recovery_seconds}" \
    '{scenario: $scenario, status: "pass", original_primary: $original_primary, replacement_primary: $replacement_primary, outage_observed: true, recovery_seconds: $recovery_seconds}' \
    >"${RESULT_FILE}"
fi

echo
echo "pg-disconnect: kubeatlas survived PG primary loss, recovered within budget."

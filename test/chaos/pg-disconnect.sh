#!/usr/bin/env bash
# Exercise a sustained storage interruption on a single-instance CNPG cluster.
# Declarative hibernation retains PVCs and prevents an unobservably short outage.
# Shutdown has its own CNPG-derived budget; application recovery must finish
# within 120s of the replacement primary becoming Ready. This is not an HA drill.
set -euo pipefail

NS="${NS:-kubeatlas}"
RELEASE="${RELEASE:-kubeatlas}"
TIER="${KUBEATLAS_TIER:-tier2}"
KUBEATLAS_PF_PORT="${KUBEATLAS_PF_PORT:-18080}"
RESULT_FILE="${KUBEATLAS_CHAOS_RESULT_FILE:-}"
CLUSTER="${RELEASE}-pg"
BASE_URL="http://127.0.0.1:${KUBEATLAS_PF_PORT}"
hibernation_owned=0

fail() { printf 'pg-disconnect: %s\n' "$*" >&2; exit 1; }
kube() { kubectl --request-timeout=10s "$@"; }
api() { curl -fsS --connect-timeout 2 --max-time 5 "${BASE_URL}$1"; }
metric_is() { awk -v key="$2" -v value="$3" '$1 == key && $2 == value {found=1} END {exit !found}' <<<"$1"; }
panic_count() {
  awk '$1 == "kubeatlas_rego_eval_panic_total" {value=$2; found=1}
    END {if (!found || value !~ /^[0-9]+$/) exit 1; print value}' <<<"$1"
}
restore_hibernation() {
  if [[ "${original_hibernation}" == "off" ]]; then
    kube annotate cluster.postgresql.cnpg.io "${CLUSTER}" -n "${NS}" --overwrite cnpg.io/hibernation=off >/dev/null
  else
    kube annotate cluster.postgresql.cnpg.io "${CLUSTER}" -n "${NS}" cnpg.io/hibernation- >/dev/null
  fi
}
cleanup() {
  local status=$? attempt restored=0
  if (( hibernation_owned == 1 )); then
    for attempt in 1 2 3; do
      if restore_hibernation; then restored=1; break; fi
      sleep 2
    done
    if (( restored == 0 )); then
      printf 'pg-disconnect: RESTORE FAILED; remove cnpg.io/hibernation from %s/%s to resume PostgreSQL\n' "${NS}" "${CLUSTER}" >&2
      status=1
    fi
  fi
  return "${status}"
}

if [[ "${TIER}" != "tier2" ]]; then
  echo "pg-disconnect: SKIPPED (KUBEATLAS_TIER=${TIER}; this scenario is Tier 2 only)"
  exit 0
fi
for cmd in kubectl curl jq date awk; do command -v "${cmd}" >/dev/null || fail "missing: ${cmd}"; done
[[ -z "${RESULT_FILE}" || ! -e "${RESULT_FILE}" ]] || fail "refusing to overwrite result file ${RESULT_FILE}"
cluster_json=$(kube get cluster.postgresql.cnpg.io "${CLUSTER}" -n "${NS}" -o json)
jq -e '.spec.instances == 1 and .status.readyInstances == 1' <<<"${cluster_json}" >/dev/null \
  || fail 'requires a healthy single-instance CNPG cluster'
original_hibernation=$(jq -r '.metadata.annotations["cnpg.io/hibernation"] // ""' <<<"${cluster_json}")
[[ -z "${original_hibernation}" || "${original_hibernation}" == "off" ]] || fail 'cluster is already hibernating'
stop_delay=$(jq -r '.spec.stopDelay // 1800' <<<"${cluster_json}")
[[ "${stop_delay}" =~ ^[0-9]+$ ]] && (( stop_delay <= 3600 )) || fail 'unsupported CNPG stopDelay (must be 0..3600 seconds)'
primary_json=$(kube get pods -n "${NS}" -l "cnpg.io/cluster=${CLUSTER},cnpg.io/instanceRole=primary" -o json)
PRIMARY=$(jq -er '.items | select(length == 1) | .[0] | select(.metadata.deletionTimestamp == null) | select(any(.status.conditions[]?; .type == "Ready" and .status == "True")) | .metadata.name' <<<"${primary_json}")
original_uid=$(jq -er '.items[0].metadata.uid' <<<"${primary_json}")
app_before=$(kube get pods -n "${NS}" -l "app.kubernetes.io/name=kubeatlas,app.kubernetes.io/instance=${RELEASE}" -o json \
  | jq -ce '[.items[] | select(.metadata.labels["pod-template-hash"] != null)] | select(length == 1) | .[0] | {uid:.metadata.uid,restarts:[.status.containerStatuses[] | {name,restartCount}]}')
metrics_before=$(api /metrics)
panic_before=$(panic_count "${metrics_before}")
metric_is "${metrics_before}" kubeatlas_storage_reachable 1 || fail 'storage probe was not healthy before chaos'
api /healthz >/dev/null

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# Set ownership before the request, since a timeout may follow an applied write.
hibernation_owned=1
injection_started_at=$(date +%s)
echo "==> Hibernating ${CLUSTER}; primary=${PRIMARY} uid=${original_uid}; shutdown budget=$((stop_delay + 120))s"
kube annotate cluster.postgresql.cnpg.io "${CLUSTER}" -n "${NS}" --overwrite cnpg.io/hibernation=on >/dev/null
deadline=$((injection_started_at + stop_delay + 120))
hibernated=0
while (( $(date +%s) < deadline )); do
  cluster_json=$(kube get cluster.postgresql.cnpg.io "${CLUSTER}" -n "${NS}" -o json)
  if jq -e 'any(.status.conditions[]?; .type == "cnpg.io/hibernation" and .status == "True")' <<<"${cluster_json}" >/dev/null; then
    pods=$(kube get pods -n "${NS}" -l "cnpg.io/cluster=${CLUSTER}" -o json)
    if jq -e '.items | length == 0' <<<"${pods}" >/dev/null; then hibernated=1; break; fi
  fi
  sleep 2
done
(( hibernated == 1 && $(date +%s) <= deadline )) || fail 'CNPG did not finish hibernation within its shutdown budget'
shutdown_seconds=$(( $(date +%s) - injection_started_at ))
echo "==> PostgreSQL stopped after ${shutdown_seconds}s; waiting up to 60s for storage outage metrics"
deadline=$(( $(date +%s) + 60 ))
outage_observed=0
while (( $(date +%s) < deadline )); do
  api /healthz >/dev/null || fail 'application liveness failed during storage outage'
  metrics_during=$(api /metrics) || fail 'application metrics unavailable during storage outage'
  if metric_is "${metrics_during}" kubeatlas_storage_reachable 0; then outage_observed=1; break; fi
  sleep 2
done
(( outage_observed == 1 && $(date +%s) <= deadline )) || fail 'storage outage was not visible in metrics within 60s of hibernation'
outage_observed_at=$(date +%s)
echo '==> Observed kubeatlas_storage_reachable 0; resuming PostgreSQL'
restore_hibernation
hibernation_owned=0
resume_started_at=$(date +%s)
deadline=$((resume_started_at + 120))
replacement_json=""
while (( $(date +%s) < deadline )); do
  pods=$(kube get pods -n "${NS}" -l "cnpg.io/cluster=${CLUSTER},cnpg.io/instanceRole=primary" -o json)
  replacement_json=$(jq -c --arg uid "${original_uid}" '[.items[] | select(.metadata.uid != $uid and .metadata.deletionTimestamp == null) | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | if length == 1 then .[0] else empty end' <<<"${pods}")
  [[ -z "${replacement_json}" ]] || break
  sleep 2
done
[[ -n "${replacement_json}" ]] && (( $(date +%s) <= deadline )) || fail 'no replacement Ready primary surfaced within 120s'
new_primary=$(jq -r '.metadata.name' <<<"${replacement_json}")
replacement_uid=$(jq -r '.metadata.uid' <<<"${replacement_json}")
recovery_started_at=$(date +%s)
replacement_seconds=$((recovery_started_at - resume_started_at))
echo "==> Replacement Ready primary=${new_primary} uid=${replacement_uid}; waiting for application recovery within 120s"
deadline=$((recovery_started_at + 120))
ok=0
while (( $(date +%s) < deadline )); do
  metrics_after=$(api /metrics) || fail 'application metrics unavailable during recovery'
  if metric_is "${metrics_after}" kubeatlas_storage_reachable 1 && api '/api/v1/graph?level=cluster' >/dev/null; then ok=1; break; fi
  sleep 2
done
recovery_seconds=$(( $(date +%s) - recovery_started_at ))
(( ok == 1 && recovery_seconds <= 120 )) || fail 'KubeAtlas storage/graph did not recover within 120s'
panic_after=$(panic_count "${metrics_after}")
[[ "${panic_after}" == "${panic_before}" ]] || fail "panic counter changed (${panic_before} -> ${panic_after})"
api /healthz >/dev/null
app_after=$(kube get pods -n "${NS}" -l "app.kubernetes.io/name=kubeatlas,app.kubernetes.io/instance=${RELEASE}" -o json \
  | jq -ce '[.items[] | select(.metadata.labels["pod-template-hash"] != null)] | select(length == 1) | .[0] | {uid:.metadata.uid,restarts:[.status.containerStatuses[] | {name,restartCount}]}')
[[ "${app_after}" == "${app_before}" ]] || fail 'application Pod or container restarted during the interruption'
if [[ -n "${RESULT_FILE}" ]]; then
  jq -n --arg scenario postgresql-interruption --arg original_primary "${PRIMARY}" --arg replacement_primary "${new_primary}" \
    --arg original_uid "${original_uid}" --arg replacement_uid "${replacement_uid}" \
    --argjson shutdown_seconds "${shutdown_seconds}" --argjson outage_observed_at "${outage_observed_at}" \
    --argjson replacement_seconds "${replacement_seconds}" --argjson recovery_seconds "${recovery_seconds}" \
    '{scenario:$scenario,status:"pass",injection:"cnpg-hibernation",original_primary:$original_primary,replacement_primary:$replacement_primary,original_primary_uid:$original_uid,replacement_primary_uid:$replacement_uid,shutdown_seconds:$shutdown_seconds,outage_observed_at_epoch:$outage_observed_at,outage_observed:true,replacement_ready_seconds:$replacement_seconds,recovery_seconds:$recovery_seconds}' >"${RESULT_FILE}"
fi
echo "pg-disconnect: application survived observed storage loss; replacement Ready after ${replacement_seconds}s; storage/graph recovered after ${recovery_seconds}s."

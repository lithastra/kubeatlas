#!/usr/bin/env bash
# test/verify/tier2-lifecycle.sh
#
# Verifies the v1.5.1 embedded Tier 2 lifecycle contract:
#   1. CNPG replaces a failed database Pod without replacing its PVC.
#   2. KubeAtlas reconnects and sees the same persisted graph.
#   3. `helm uninstall` removes the application but deliberately keeps
#      the CNPG Cluster and PVC until an operator deletes them explicitly.

set -euo pipefail

NS="${KUBEATLAS_NAMESPACE:-kubeatlas}"
RELEASE="${KUBEATLAS_RELEASE:-kubeatlas}"
PG_CLUSTER="${KUBEATLAS_PG_CLUSTER:-${RELEASE}-pg}"
PF_PORT="${KUBEATLAS_PF_PORT:-18081}"
PF_PID=""

red()    { printf '\033[31m%s\033[0m\n' "$*" >&2; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }

step() { yellow "▶ $*"; }
pass() { green "  ✓ $*"; }
fail() { red "  ✗ $*"; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 \
    || fail "missing required tool: $1"
}

stop_port_forward() {
  if [[ -n "${PF_PID}" ]] && kill -0 "${PF_PID}" 2>/dev/null; then
    kill "${PF_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
  fi
  PF_PID=""
}

start_port_forward() {
  kubectl port-forward -n "${NS}" "deploy/${RELEASE}" \
    "${PF_PORT}:8080" >/tmp/kubeatlas-tier2-lifecycle-pf.log 2>&1 &
  PF_PID=$!
  trap stop_port_forward EXIT

  for _ in $(seq 1 60); do
    if curl -fsS --max-time 1 \
      "http://127.0.0.1:${PF_PORT}/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "KubeAtlas API did not become reachable on :${PF_PORT}"
}

api_get() {
  curl -fsS --max-time 10 "http://127.0.0.1:${PF_PORT}$1"
}

pvc_uids() {
  kubectl get pvc -n "${NS}" \
    -l "cnpg.io/cluster=${PG_CLUSTER}" \
    -o json \
    | jq -r '.items | sort_by(.metadata.name) | map(.metadata.uid) | join(",")'
}

for cmd in kubectl helm jq curl; do
  require_cmd "${cmd}"
done

step "preflight: CNPG Cluster is Ready and protected from Helm uninstall"
kubectl wait -n "${NS}" \
  --for=condition=Ready \
  "clusters.postgresql.cnpg.io/${PG_CLUSTER}" \
  --timeout=3m >/dev/null
retention_policy="$(
  kubectl get -n "${NS}" \
    "clusters.postgresql.cnpg.io/${PG_CLUSTER}" \
    -o jsonpath='{.metadata.annotations.helm\.sh/resource-policy}'
)"
[[ "${retention_policy}" == "keep" ]] \
  || fail "expected helm.sh/resource-policy=keep, got ${retention_policy:-<empty>}"
pass "Cluster Ready; uninstall policy is keep"

before_pvc_uids="$(pvc_uids)"
[[ -n "${before_pvc_uids}" ]] || fail "no CNPG PVC found"

start_port_forward
before_resources="$(
  api_get '/api/v1alpha1/graph?level=cluster' \
    | jq -r '.resources | length'
)"
[[ "${before_resources}" =~ ^[0-9]+$ ]] \
  || fail "pre-failure graph count is not numeric: ${before_resources}"

step "failure recovery: delete the CNPG instance Pod"
old_pg_pod="$(
  kubectl get pod -n "${NS}" \
    -l "cnpg.io/cluster=${PG_CLUSTER}" \
    -o json \
    | jq -r '.items[0].metadata.name // empty'
)"
[[ -n "${old_pg_pod}" ]] || fail "no CNPG instance Pod found"
old_pg_uid="$(
  kubectl get pod -n "${NS}" "${old_pg_pod}" \
    -o jsonpath='{.metadata.uid}'
)"
# A normal Pod deletion follows CNPG's 180-second smart-shutdown
# window. This test models an actual instance failure instead: the
# process disappears immediately and PostgreSQL must recover from
# the existing WAL and PVC.
kubectl delete pod -n "${NS}" "${old_pg_pod}" \
  --force --grace-period=0 --wait=false >/dev/null

replacement=""
replacement_uid=""
deadline=$((SECONDS + 180))
while (( SECONDS < deadline )); do
  replacement_record="$(
    kubectl get pod -n "${NS}" \
      -l "cnpg.io/cluster=${PG_CLUSTER}" \
      -o json 2>/dev/null \
      | jq -r --arg old_uid "${old_pg_uid}" '
          .items[]
          | select(.metadata.uid != $old_uid)
          | select(any(.status.conditions[]?;
              .type == "Ready" and .status == "True"))
          | [.metadata.name, .metadata.uid]
          | @tsv
        ' \
      | head -n 1
  )"
  if [[ -n "${replacement_record}" ]]; then
    IFS=$'\t' read -r replacement replacement_uid \
      <<<"${replacement_record}"
    break
  fi
  sleep 2
done
[[ -n "${replacement}" ]] \
  || fail "CNPG did not replace Pod UID ${old_pg_uid}"
pass "CNPG replaced ${old_pg_pod} (${old_pg_uid}) with ${replacement} (${replacement_uid})"

after_pvc_uids="$(pvc_uids)"
[[ "${after_pvc_uids}" == "${before_pvc_uids}" ]] \
  || fail "PVC identity changed after Pod failure"

step "data recovery: KubeAtlas reconnects to the same graph"
after_resources=""
deadline=$((SECONDS + 120))
while (( SECONDS < deadline )); do
  after_resources="$(
    api_get '/api/v1alpha1/graph?level=cluster' 2>/dev/null \
      | jq -r '.resources | length' 2>/dev/null \
      || true
  )"
  [[ "${after_resources}" == "${before_resources}" ]] && break
  sleep 2
done
[[ "${after_resources}" == "${before_resources}" ]] \
  || fail "graph did not converge after database Pod recovery"
pass "same PVC and graph count survived database Pod replacement"

stop_port_forward
trap - EXIT

step "uninstall: remove KubeAtlas while retaining the database"
helm uninstall "${RELEASE}" -n "${NS}" >/dev/null
if kubectl get deployment -n "${NS}" "${RELEASE}" >/dev/null 2>&1; then
  fail "KubeAtlas Deployment still exists after helm uninstall"
fi
kubectl get -n "${NS}" \
  "clusters.postgresql.cnpg.io/${PG_CLUSTER}" >/dev/null
final_pvc_uids="$(pvc_uids)"
[[ "${final_pvc_uids}" == "${before_pvc_uids}" ]] \
  || fail "PVC identity changed during helm uninstall"
pass "application removed; CNPG Cluster and PVC retained"

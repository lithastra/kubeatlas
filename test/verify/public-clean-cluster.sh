#!/usr/bin/env bash

# Install the current anonymous public KubeAtlas release from GitHub and the
# Helm OCI registry into a clean vanilla Kubernetes cluster. The caller owns
# the cluster and the external CloudNativePG operator. This verifier writes
# only a deliberately bounded evidence file suitable for failure retention.

set -euo pipefail

PUBLIC_VERSION=${KUBEATLAS_PUBLIC_VERSION:?set KUBEATLAS_PUBLIC_VERSION}
PUBLIC_BINARY=${KUBEATLAS_PUBLIC_BINARY:?set KUBEATLAS_PUBLIC_BINARY}
EXPECTED_CNPG_OPERATOR=${KUBEATLAS_CNPG_OPERATOR_VERSION:-1.30.0}
NS=${KUBEATLAS_NAMESPACE:-kubeatlas-public-clean}
TIER1_RELEASE=${KUBEATLAS_TIER1_RELEASE:-kubeatlas-public-tier1}
TIER2_RELEASE=${KUBEATLAS_TIER2_RELEASE:-kubeatlas-public}
CHART=oci://ghcr.io/lithastra/charts/kubeatlas
EVIDENCE=${KUBEATLAS_EVIDENCE_FILE:-/tmp/kubeatlas-public-clean-evidence.log}
PF_PORT=${KUBEATLAS_PF_PORT:-18084}
PF_PID=""
SECRET_SENTINEL="public-clean-$(openssl rand -hex 24)"
SECRET_SENTINEL_B64=$(printf '%s' "${SECRET_SENTINEL}" | base64 | tr -d '\n')
DB_PASSWORD=""

fail() {
  printf 'public clean cluster: %s\n' "$*" >&2
  exit 1
}

stop_port_forward() {
  if [[ -n "${PF_PID}" ]] && kill -0 "${PF_PID}" 2>/dev/null; then
    kill "${PF_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
  fi
  PF_PID=""
}

sanitize_stream() {
  local line
  while IFS= read -r line || [[ -n "${line}" ]]; do
    line=${line//"${SECRET_SENTINEL}"/[REDACTED]}
    line=${line//"${SECRET_SENTINEL_B64}"/[REDACTED]}
    if [[ -n "${DB_PASSWORD}" ]]; then
      line=${line//"${DB_PASSWORD}"/[REDACTED]}
    fi
    printf '%s\n' "${line}"
  done
}

sanitize_checked_stream() {
  local line
  local leaked=0
  while IFS= read -r line || [[ -n "${line}" ]]; do
    if [[ "${line}" == *"${SECRET_SENTINEL}"* ||
          "${line}" == *"${SECRET_SENTINEL_B64}"* ||
          ( -n "${DB_PASSWORD}" && "${line}" == *"${DB_PASSWORD}"* ) ]]; then
      leaked=1
    fi
    line=${line//"${SECRET_SENTINEL}"/[REDACTED]}
    line=${line//"${SECRET_SENTINEL_B64}"/[REDACTED]}
    if [[ -n "${DB_PASSWORD}" ]]; then
      line=${line//"${DB_PASSWORD}"/[REDACTED]}
    fi
    printf '%s\n' "${line}"
  done
  if (( leaked == 1 )); then
    printf 'public clean cluster: sensitive value reached a gated surface\n' >&2
    return 1
  fi
}

collect_failure_evidence() {
  if [[ -z "${DB_PASSWORD}" ]]; then
    DB_PASSWORD=$(kubectl get secret -n "${NS}" "${TIER2_RELEASE}-pg-app" \
      -o jsonpath='{.data.password}' 2>/dev/null | base64 --decode || true)
  fi
  {
    printf '%s\n' 'failure_cluster_state'
    kubectl get nodes -o wide || true
    kubectl get pods -A -o wide || true
    kubectl get clusters.postgresql.cnpg.io -A -o wide || true
    kubectl logs -n "${NS}" deployment/"${TIER2_RELEASE}" \
      --all-containers=true --tail=300 || true
    kubectl logs -n cnpg-system deployment/cnpg-cloudnative-pg \
      --all-containers=true --tail=300 || true
  } 2>&1 | sanitize_stream >>"${EVIDENCE}"
}

cleanup() {
  local status=$?
  stop_port_forward
  if (( status != 0 )); then
    collect_failure_evidence || true
  fi
  helm uninstall "${TIER1_RELEASE}" -n "${NS}" >/dev/null 2>&1 || true
  helm uninstall "${TIER2_RELEASE}" -n "${NS}" >/dev/null 2>&1 || true
  kubectl delete namespace "${NS}" --wait=false >/dev/null 2>&1 || true
  if grep -Fq -- "${SECRET_SENTINEL}" "${EVIDENCE}" ||
     grep -Fq -- "${SECRET_SENTINEL_B64}" "${EVIDENCE}" ||
     { [[ -n "${DB_PASSWORD}" ]] && grep -Fq -- "${DB_PASSWORD}" "${EVIDENCE}"; }; then
    printf 'public clean cluster: sensitive sentinel reached retained evidence\n' >&2
    return 1
  fi
  return "${status}"
}
trap cleanup EXIT

for command_name in kubectl helm curl jq openssl grep awk base64; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "missing required command: ${command_name}"
done
[[ -x "${PUBLIC_BINARY}" ]] || fail "public binary is not executable: ${PUBLIC_BINARY}"
: >"${EVIDENCE}"

printf 'public_version=%s\n' "${PUBLIC_VERSION}" >>"${EVIDENCE}"
"${PUBLIC_BINARY}" -version | sanitize_checked_stream >>"${EVIDENCE}"
kubectl version -o json | jq -c '{serverVersion: .serverVersion.gitVersion}' >>"${EVIDENCE}"

operator_image=$(kubectl get deployment -n cnpg-system cnpg-cloudnative-pg \
  -o jsonpath='{.spec.template.spec.containers[0].image}')
[[ "${operator_image##*:}" == "${EXPECTED_CNPG_OPERATOR}" ]] ||
  fail "CloudNativePG operator is ${operator_image}, expected ${EXPECTED_CNPG_OPERATOR}"
printf 'cnpg_operator_image=%s\n' "${operator_image}" >>"${EVIDENCE}"

kubectl create namespace "${NS}"
kubectl create secret generic evidence-sentinel -n "${NS}" \
  --from-literal=value="${SECRET_SENTINEL}" >/dev/null
kubectl create deployment evidence-consumer -n "${NS}" \
  --image=registry.k8s.io/pause:3.10 >/dev/null
kubectl set env deployment/evidence-consumer -n "${NS}" \
  --from=secret/evidence-sentinel >/dev/null

helm install "${TIER1_RELEASE}" "${CHART}" --version "${PUBLIC_VERSION}" \
  --namespace "${NS}" --wait --timeout 8m
kubectl rollout status deployment/"${TIER1_RELEASE}" -n "${NS}" --timeout=2m
tier1_image=$(kubectl get deployment -n "${NS}" "${TIER1_RELEASE}" \
  -o jsonpath='{.spec.template.spec.containers[0].image}')
[[ "${tier1_image}" == "ghcr.io/lithastra/kubeatlas:${PUBLIC_VERSION}" ]] ||
  fail "Tier 1 image is ${tier1_image}, expected public ${PUBLIC_VERSION}"
printf 'tier1_image=%s\n' "${tier1_image}" >>"${EVIDENCE}"
helm uninstall "${TIER1_RELEASE}" -n "${NS}" --wait --timeout 3m

helm install "${TIER2_RELEASE}" "${CHART}" --version "${PUBLIC_VERSION}" \
  --namespace "${NS}" \
  --set persistence.enabled=true \
  --set persistence.embedded.enabled=true \
  --set persistence.embedded.retainOnDelete=false \
  --set persistence.embedded.storageSize=1Gi \
  --set snapshots.enabled=true \
  --wait --timeout 10m
kubectl rollout status deployment/"${TIER2_RELEASE}" -n "${NS}" --timeout=2m

DB_PASSWORD=$(kubectl get secret -n "${NS}" "${TIER2_RELEASE}-pg-app" \
  -o jsonpath='{.data.password}' | base64 --decode)
tier2_image=$(kubectl get deployment -n "${NS}" "${TIER2_RELEASE}" \
  -o jsonpath='{.spec.template.spec.containers[0].image}')
pg_image=$(kubectl get cluster.postgresql.cnpg.io -n "${NS}" "${TIER2_RELEASE}-pg" \
  -o jsonpath='{.spec.imageName}')
[[ "${tier2_image}" == "ghcr.io/lithastra/kubeatlas:${PUBLIC_VERSION}" ]] ||
  fail "Tier 2 image is ${tier2_image}, expected public ${PUBLIC_VERSION}"
[[ -n "${pg_image}" ]] || fail "embedded PostgreSQL image is empty"
printf 'tier2_image=%s\npostgres_age_image=%s\n' "${tier2_image}" "${pg_image}" >>"${EVIDENCE}"

kubectl port-forward -n "${NS}" service/"${TIER2_RELEASE}" "${PF_PORT}:80" \
  >/tmp/kubeatlas-public-clean-port-forward.log 2>&1 &
PF_PID=$!
for _ in $(seq 1 60); do
  if curl -fsS --max-time 1 "http://127.0.0.1:${PF_PORT}/readyz" >/dev/null 2>&1; then
    break
  fi
  kill -0 "${PF_PID}" 2>/dev/null || fail "port-forward exited early"
  sleep 1
done
curl -fsS --max-time 3 "http://127.0.0.1:${PF_PORT}/readyz" >/dev/null ||
  fail "public Tier 2 release did not become ready"

metrics=$(curl -fsS --max-time 5 "http://127.0.0.1:${PF_PORT}/metrics")
grep -Fq 'kubeatlas_informer_synced 1' <<<"${metrics}" || fail "initial sync metric is not 1"
grep -Fq 'kubeatlas_snapshot_queue_drop_total' <<<"${metrics}" || fail "snapshot drop metric is absent"
printf '%s\n' "${metrics}" | sanitize_checked_stream >>"${EVIDENCE}"

# Once v1.6 becomes the current public release, the same weekly job begins
# enforcing the new continuous dependency signals without a workflow edit.
major=${PUBLIC_VERSION%%.*}
minor_patch=${PUBLIC_VERSION#*.}
minor=${minor_patch%%.*}
if (( major > 1 || (major == 1 && minor >= 6) )); then
  for metric in \
    kubeatlas_graph_observation_state \
    kubeatlas_kubernetes_api_reachable \
    kubeatlas_storage_reachable \
    kubeatlas_storage_durable \
    kubeatlas_backup_status_available; do
    grep -Fq "${metric}" <<<"${metrics}" || fail "v1.6 operational metric is absent: ${metric}"
  done
  grep -Fq 'kubeatlas_storage_durable 1' <<<"${metrics}" || fail "Tier 2 durability metric is not 1"
fi

curl -fsS --max-time 10 \
  "http://127.0.0.1:${PF_PORT}/api/v1/resources/${NS}/Secret/evidence-sentinel" \
  | sanitize_checked_stream >>"${EVIDENCE}"

{
  printf '%s\n' 'kubeatlas_logs'
  kubectl logs -n "${NS}" deployment/"${TIER2_RELEASE}" \
    --all-containers=true --tail=300 || true
  printf '%s\n' 'cnpg_operator_logs'
  kubectl logs -n cnpg-system deployment/cnpg-cloudnative-pg \
    --all-containers=true --tail=300 || true
} 2>&1 | sanitize_checked_stream >>"${EVIDENCE}"

printf 'public_clean_cluster=pass\n' >>"${EVIDENCE}"
printf 'public clean cluster: public GitHub binary and OCI Tier 1/Tier 2 passed\n'

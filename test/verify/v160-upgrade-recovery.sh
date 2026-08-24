#!/usr/bin/env bash

# Prove the supported embedded Tier 2 path from the anonymous public v1.5.2
# artifacts to the current v1.6 candidate, then destroy the original CNPG
# database and restore a protected logical backup into a fresh Cluster.
#
# The caller must provide a Kubernetes cluster with CloudNativePG 1.30.0 and
# the two candidate images already available to the nodes. This script never
# uploads the backup: the archive and its rendered SQL are exact /tmp files
# removed by the EXIT trap, including on failure.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "${ROOT_DIR}"

NS="${KUBEATLAS_NAMESPACE:-kubeatlas-upgrade-recovery}"
RELEASE="${KUBEATLAS_RELEASE:-kubeatlas-recovery}"
PG_CLUSTER="${KUBEATLAS_PG_CLUSTER:-${RELEASE}-pg}"
CANDIDATE_IMAGE="${KUBEATLAS_CANDIDATE_IMAGE:-kubeatlas:upgrade-recovery}"
CANDIDATE_PG_IMAGE="${KUBEATLAS_CANDIDATE_PG_IMAGE:-local-postgres-age:16.15-age1.6.0-rc0.2}"
PUBLIC_CHART="oci://ghcr.io/lithastra/charts/kubeatlas"
PUBLIC_VERSION=1.5.2
PUBLIC_APP_IMAGE="ghcr.io/lithastra/kubeatlas:1.5.2"
PUBLIC_PG_IMAGE="ghcr.io/lithastra/postgres-age:16.6-age1.6.0-rc0.1"
PF_PORT="${KUBEATLAS_PF_PORT:-18083}"
PF_PID=""
BACKUP_FILE=/tmp/kubeatlas-v152-upgrade-recovery.dump
BACKUP_SQL_FILE=/tmp/kubeatlas-v152-upgrade-recovery.sql
RESTORED_SQL_FILE=/tmp/kubeatlas-v160-restored.sql
SURFACES_FILE=/tmp/kubeatlas-upgrade-recovery-surfaces.log
DUMP_LOG=/tmp/kubeatlas-upgrade-recovery-pg-dump.log
RESTORE_LOG=/tmp/kubeatlas-upgrade-recovery-pg-restore.log
POD_BACKUP=/tmp/kubeatlas-v152-upgrade-recovery.dump
DIAGNOSTICS_LOG=/tmp/kubeatlas-upgrade-recovery-diagnostics.log
SECRET_SENTINEL=""
SECRET_SENTINEL_B64=""
source_password=""
restored_password=""

red()    { printf '\033[31m%s\033[0m\n' "$*" >&2; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
step()   { yellow "==> $*"; }
pass()   { green "PASS: $*"; }
fail()   { red "FAIL: $*"; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

stop_port_forward() {
  if [[ -n "${PF_PID}" ]] && kill -0 "${PF_PID}" 2>/dev/null; then
    kill "${PF_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
  fi
  PF_PID=""
}

cleanup_sensitive_files() {
  local status=$?
  stop_port_forward
  if (( status != 0 )); then
    collect_failure_diagnostics || true
    sanitize_file "${DUMP_LOG}"
    sanitize_file "${RESTORE_LOG}"
  fi
  for path in \
    "${BACKUP_FILE}" \
    "${BACKUP_SQL_FILE}" \
    "${RESTORED_SQL_FILE}" \
    "${SURFACES_FILE}"; do
    if [[ -f "${path}" ]]; then
      unlink "${path}"
    fi
  done
  return "${status}"
}
trap cleanup_sensitive_files EXIT

sanitize_stream() {
  local line
  local value
  while IFS= read -r line || [[ -n "${line}" ]]; do
    for value in \
      "${SECRET_SENTINEL}" \
      "${SECRET_SENTINEL_B64}" \
      "${source_password}" \
      "${restored_password}"; do
      if [[ -n "${value}" ]]; then
        line=${line//"${value}"/[REDACTED]}
      fi
    done
    printf '%s\n' "${line}"
  done
}

sanitize_file() {
  local file=$1
  local sanitized="${file}.sanitized"
  if [[ ! -f "${file}" ]]; then
    return 0
  fi
  sanitize_stream <"${file}" >"${sanitized}"
  mv "${sanitized}" "${file}"
}

collect_failure_diagnostics() {
  {
    printf '%s\n' 'KubeAtlas v1.6 upgrade/recovery failure diagnostics'
    kubectl get pods -A -o wide || true
    kubectl get clusters.postgresql.cnpg.io -A -o wide || true
    kubectl get pvc -A -o wide || true
    kubectl describe pods -n "${NS}" || true
    kubectl logs -n "${NS}" deployment/"${RELEASE}" \
      --all-containers=true --tail=300 || true
    kubectl logs -n cnpg-system deployment/cnpg-cloudnative-pg \
      --all-containers=true --tail=300 || true
    helm status "${RELEASE}" -n "${NS}" || true
  } 2>&1 | sanitize_stream >"${DIAGNOSTICS_LOG}"
}

primary_pod() {
  kubectl get pods -n "${NS}" \
    -l "cnpg.io/cluster=${PG_CLUSTER},cnpg.io/instanceRole=primary" \
    -o json 2>/dev/null \
    | jq -r '
        [.items[]
          | select(any(.status.conditions[]?;
              .type == "Ready" and .status == "True"))]
        | sort_by(.metadata.creationTimestamp)
        | last
        | .metadata.name // empty'
}

wait_for_primary() {
  local timeout=$1
  local deadline=$((SECONDS + timeout))
  local pod
  while (( SECONDS < deadline )); do
    pod=$(primary_pod)
    if [[ -n "${pod}" ]]; then
      printf '%s\n' "${pod}"
      return 0
    fi
    sleep 2
  done
  return 1
}

sql_value() {
  local query=$1
  local pod
  pod=$(primary_pod)
  [[ -n "${pod}" ]] || fail "no Ready primary Pod for ${PG_CLUSTER}"
  kubectl exec -n "${NS}" "${pod}" -c postgres -- \
    psql -v ON_ERROR_STOP=1 -U postgres -d kubeatlas -Atc "${query}" \
    | tr -d '[:space:]'
}

wait_sql_ge() {
  local description=$1
  local query=$2
  local minimum=$3
  local timeout=${4:-90}
  local deadline=$((SECONDS + timeout))
  local actual=""
  while (( SECONDS < deadline )); do
    actual=$(sql_value "${query}" 2>/dev/null || true)
    if [[ "${actual}" =~ ^[0-9]+$ ]] && (( actual >= minimum )); then
      pass "${description}: ${actual} >= ${minimum}"
      return 0
    fi
    sleep 2
  done
  fail "${description}: got ${actual:-<empty>}, want >= ${minimum}"
}

start_port_forward() {
  stop_port_forward
  kubectl port-forward -n "${NS}" "service/${RELEASE}" \
    "${PF_PORT}:80" >/tmp/kubeatlas-upgrade-recovery-port-forward.log 2>&1 &
  PF_PID=$!
  for _ in $(seq 1 60); do
    if curl -fsS --max-time 1 \
      "http://127.0.0.1:${PF_PORT}/readyz" >/dev/null 2>&1; then
      return 0
    fi
    if ! kill -0 "${PF_PID}" 2>/dev/null; then
      cat /tmp/kubeatlas-upgrade-recovery-port-forward.log >&2
      fail "port-forward exited before KubeAtlas became ready"
    fi
    sleep 1
  done
  cat /tmp/kubeatlas-upgrade-recovery-port-forward.log >&2
  fail "KubeAtlas did not become reachable on ${PF_PORT}"
}

api_get() {
  curl -fsS --max-time 20 "http://127.0.0.1:${PF_PORT}$1"
}

trigger_snapshot() {
  local suffix=$1
  local job_name="${RELEASE}-snapshot-${suffix}"
  kubectl delete job -n "${NS}" "${job_name}" --ignore-not-found >/dev/null
  kubectl create job -n "${NS}" "${job_name}" \
    --from="cronjob/${RELEASE}-snapshot" >/dev/null
  kubectl wait -n "${NS}" --for=condition=complete \
    "job/${job_name}" --timeout=2m >/dev/null
}

assert_absent_file() {
  local description=$1
  local value=$2
  local file=$3
  [[ -n "${value}" ]] || fail "${description}: empty sentinel cannot be checked"
  if grep -Fq -- "${value}" "${file}"; then
    fail "${description} reached ${file}"
  fi
}

assert_sensitive_values_absent() {
  local file=$1
  local source_password=$2
  local restored_password=${3:-}
  assert_absent_file "raw Secret sentinel" "${SECRET_SENTINEL}" "${file}"
  assert_absent_file "base64 Secret sentinel" "${SECRET_SENTINEL_B64}" "${file}"
  assert_absent_file "source database credential" "${source_password}" "${file}"
  if [[ -n "${restored_password}" ]]; then
    assert_absent_file "restored database credential" "${restored_password}" "${file}"
  fi
}

collect_security_surfaces() {
  : >"${SURFACES_FILE}"
  for endpoint in \
    '/api/v1/graph?level=cluster' \
    "/api/v1/resources/${NS}/Secret/recovery-sentinel" \
    '/api/v1/snapshots' \
    "/api/v1/snapshots/diff?from=24h&to=now&namespace=${NS}" \
    "/api/v1/diagnose?namespace=${NS}" \
    "/api/v1alpha1/export?format=svg&namespace=${NS}" \
    '/api/v1/policy/constraints' \
    '/api/v1/telemetry/status' \
    '/api/v1/telemetry/preview' \
    '/metrics'; do
    api_get "${endpoint}" >>"${SURFACES_FILE}"
    printf '\n' >>"${SURFACES_FILE}"
  done
  while IFS= read -r pod; do
    kubectl logs -n "${NS}" "${pod}" --all-containers=true \
      >>"${SURFACES_FILE}" 2>&1 || true
  done < <(kubectl get pods -n "${NS}" -o name)
}

for command_name in kubectl helm jq curl grep openssl; do
  require_cmd "${command_name}"
done
if command -v sha256sum >/dev/null 2>&1; then
  sha256_file() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256_file() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  fail "missing required SHA-256 command: sha256sum or shasum"
fi

SECRET_SENTINEL=$(openssl rand -hex 32)
SECRET_SENTINEL_B64=$(printf '%s' "${SECRET_SENTINEL}" | openssl base64 -A)

step "verify the anonymous public v1.5.2 chart contract"
public_chart_metadata=$(helm show chart "${PUBLIC_CHART}" --version "${PUBLIC_VERSION}")
grep -Fxq "version: ${PUBLIC_VERSION}" <<<"${public_chart_metadata}" \
  || fail "public chart version is not ${PUBLIC_VERSION}"
grep -Fxq "appVersion: ${PUBLIC_VERSION}" <<<"${public_chart_metadata}" \
  || fail "public chart appVersion is not ${PUBLIC_VERSION}"
pass "public chart ${PUBLIC_VERSION} resolved without registry credentials"

step "create representative Kubernetes data and a random Secret sentinel"
kubectl create namespace "${NS}"
kubectl create configmap recovery-state -n "${NS}" \
  --from-literal=phase=before-backup >/dev/null
kubectl create secret generic recovery-sentinel -n "${NS}" \
  --from-literal=token="${SECRET_SENTINEL}" >/dev/null
kubectl create deployment recovery-consumer -n "${NS}" \
  --image=registry.k8s.io/pause:3.10 >/dev/null
kubectl set env deployment/recovery-consumer -n "${NS}" \
  --from=configmap/recovery-state >/dev/null
kubectl set env deployment/recovery-consumer -n "${NS}" \
  --from=secret/recovery-sentinel >/dev/null
pass "fixture contains ConfigMap and Secret relationships"

step "install the real public v1.5.2 chart, application, and database image"
helm install "${RELEASE}" "${PUBLIC_CHART}" \
  --version "${PUBLIC_VERSION}" \
  --namespace "${NS}" \
  --set persistence.enabled=true \
  --set persistence.embedded.enabled=true \
  --set persistence.embedded.storageSize=1Gi \
  --set snapshots.enabled=true \
  --wait --timeout 10m

deployed_app_image=$(kubectl get deployment -n "${NS}" "${RELEASE}" \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="kubeatlas")].image}')
deployed_pg_image=$(kubectl get "clusters.postgresql.cnpg.io/${PG_CLUSTER}" \
  -n "${NS}" -o jsonpath='{.spec.imageName}')
[[ "${deployed_app_image}" == "${PUBLIC_APP_IMAGE}" ]] \
  || fail "public application image is ${deployed_app_image}, want ${PUBLIC_APP_IMAGE}"
[[ "${deployed_pg_image}" == "${PUBLIC_PG_IMAGE}" ]] \
  || fail "public database image is ${deployed_pg_image}, want ${PUBLIC_PG_IMAGE}"
public_app_image_id=$(kubectl get pod -n "${NS}" \
  -l "app.kubernetes.io/name=kubeatlas,app.kubernetes.io/instance=${RELEASE}" \
  -o json | jq -r '[.items[].status.containerStatuses[]? | select(.name == "kubeatlas")][0].imageID // empty')
public_pg_image_id=$(kubectl get pod -n "${NS}" \
  -l "cnpg.io/cluster=${PG_CLUSTER}" -o json \
  | jq -r '[.items[].status.containerStatuses[]? | select(.name == "postgres")][0].imageID // empty')
[[ "${public_app_image_id}" == *@sha256:* ]] || fail "public application imageID is not digest-resolved"
[[ "${public_pg_image_id}" == *@sha256:* ]] || fail "public database imageID is not digest-resolved"
pass "public artifacts are running by immutable platform manifests"

step "seed current graph relationships and representative snapshot history"
wait_sql_ge "current ConfigMap row" \
  "SELECT count(*) FROM public.resources WHERE id = '${NS}/ConfigMap/recovery-state'" 1
wait_sql_ge "ConfigMap relationship" \
  "SELECT count(*) FROM public.edges WHERE from_id = '${NS}/Deployment/recovery-consumer' AND to_id = '${NS}/ConfigMap/recovery-state'" 1
wait_sql_ge "Secret reference relationship" \
  "SELECT count(*) FROM public.edges WHERE from_id = '${NS}/Deployment/recovery-consumer' AND to_id = '${NS}/Secret/recovery-sentinel'" 1
trigger_snapshot before-update
kubectl patch configmap recovery-state -n "${NS}" --type=merge \
  -p '{"data":{"phase":"at-backup"}}' >/dev/null
wait_sql_ge "ConfigMap history" \
  "SELECT count(*) FROM public.resource_events WHERE namespace = '${NS}' AND kind = 'ConfigMap' AND name = 'recovery-state'" 2
trigger_snapshot at-backup
wait_sql_ge "snapshot markers" \
  "SELECT count(*) FROM public.snapshot_meta" 2

source_password=$(kubectl get secret -n "${NS}" "${PG_CLUSTER}-app" \
  -o jsonpath='{.data.password}' | openssl base64 -d -A)
[[ -n "${source_password}" ]] || fail "source CNPG application password is empty"

step "freeze the writer and create a protected pre-upgrade custom-format dump"
stop_port_forward
kubectl scale deployment -n "${NS}" "${RELEASE}" --replicas=0 >/dev/null
kubectl rollout status deployment -n "${NS}" "${RELEASE}" --timeout=2m >/dev/null
schema_before=$(sql_value 'SELECT max(version) FROM public.schema_migrations')
history_before=$(sql_value "SELECT count(*) FROM public.resource_events WHERE namespace = '${NS}'")
snapshots_before=$(sql_value 'SELECT count(*) FROM public.snapshot_meta')
edges_before=$(sql_value "SELECT count(*) FROM public.edges WHERE from_id = '${NS}/Deployment/recovery-consumer'")
[[ "${schema_before}" == "11" ]] || fail "public v1.5.2 schema is ${schema_before}, want 11"
(( history_before >= 2 )) || fail "representative history was not seeded"
(( snapshots_before >= 2 )) || fail "representative snapshots were not seeded"
(( edges_before >= 2 )) || fail "representative relationships were not seeded"
source_pg_pod=$(primary_pod)
[[ -n "${source_pg_pod}" ]] || fail "source PostgreSQL primary is not Ready"
if ! kubectl exec -n "${NS}" "${source_pg_pod}" -c postgres -- \
  pg_dump -Fc -U postgres -d kubeatlas >"${BACKUP_FILE}" 2>"${DUMP_LOG}"; then
  fail "pg_dump failed; see ${DUMP_LOG}"
fi
[[ -s "${BACKUP_FILE}" ]] || fail "protected backup is empty"
kubectl exec -i -n "${NS}" "${source_pg_pod}" -c postgres -- \
  pg_restore --file=- <"${BACKUP_FILE}" >"${BACKUP_SQL_FILE}"
assert_sensitive_values_absent "${BACKUP_SQL_FILE}" "${source_password}"
backup_sha=$(sha256_file "${BACKUP_FILE}")
pass "backup is readable, sentinel-free, and protected by checksum ${backup_sha}"

step "upgrade the database image and KubeAtlas application to the candidate"
helm upgrade "${RELEASE}" helm/kubeatlas \
  --namespace "${NS}" \
  --reuse-values \
  --set image.repository="${CANDIDATE_IMAGE%:*}" \
  --set image.tag="${CANDIDATE_IMAGE##*:}" \
  --set image.pullPolicy=Never \
  --set persistence.enabled=true \
  --set persistence.embedded.enabled=true \
  --set persistence.embedded.image="${CANDIDATE_PG_IMAGE}" \
  --set persistence.embedded.storageSize=1Gi \
  --set snapshots.enabled=true \
  --wait --timeout 10m

candidate_app_image=$(kubectl get deployment -n "${NS}" "${RELEASE}" \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="kubeatlas")].image}')
candidate_pg_image=$(kubectl get "clusters.postgresql.cnpg.io/${PG_CLUSTER}" \
  -n "${NS}" -o jsonpath='{.spec.imageName}')
[[ "${candidate_app_image}" == "${CANDIDATE_IMAGE}" ]] \
  || fail "candidate application image is ${candidate_app_image}"
[[ "${candidate_pg_image}" == "${CANDIDATE_PG_IMAGE}" ]] \
  || fail "candidate database image is ${candidate_pg_image}"
candidate_pg_pod=$(primary_pod)
candidate_pg_version=$(kubectl exec -n "${NS}" "${candidate_pg_pod}" -c postgres -- \
  postgres --version)
[[ "${candidate_pg_version}" == *" 16.15"* ]] \
  || fail "candidate PostgreSQL version is ${candidate_pg_version}"
[[ "$(sql_value 'SELECT max(version) FROM public.schema_migrations')" == "11" ]] \
  || fail "candidate schema migration did not complete"
pass "database image and application upgrades completed in order"

step "create current Kubernetes state that is newer than the backup"
kubectl create configmap post-backup-only -n "${NS}" \
  --from-literal=source=kubernetes-resync >/dev/null
wait_sql_ge "post-backup current resource before loss" \
  "SELECT count(*) FROM public.resources WHERE id = '${NS}/ConfigMap/post-backup-only'" 1

step "deliberately remove the original database and PVC"
kubectl scale deployment -n "${NS}" "${RELEASE}" --replicas=0 >/dev/null
kubectl rollout status deployment -n "${NS}" "${RELEASE}" --timeout=2m >/dev/null
old_cluster_uid=$(kubectl get "clusters.postgresql.cnpg.io/${PG_CLUSTER}" \
  -n "${NS}" -o jsonpath='{.metadata.uid}')
old_pvc_uid=$(kubectl get pvc -n "${NS}" -l "cnpg.io/cluster=${PG_CLUSTER}" \
  -o json | jq -r '.items[0].metadata.uid // empty')
[[ -n "${old_cluster_uid}" && -n "${old_pvc_uid}" ]] \
  || fail "original Cluster or PVC UID is empty"
kubectl delete "clusters.postgresql.cnpg.io/${PG_CLUSTER}" -n "${NS}" \
  --wait=true --timeout=5m >/dev/null
kubectl wait -n "${NS}" --for=delete pvc \
  -l "cnpg.io/cluster=${PG_CLUSTER}" --timeout=5m >/dev/null
pass "original database ${old_cluster_uid} and PVC ${old_pvc_uid} were deleted"

step "create a fresh embedded target from the candidate chart contract"
helm template "${RELEASE}" helm/kubeatlas \
  --namespace "${NS}" \
  --show-only templates/postgres-cluster.yaml \
  --set persistence.enabled=true \
  --set persistence.embedded.enabled=true \
  --set persistence.embedded.image="${CANDIDATE_PG_IMAGE}" \
  --set persistence.embedded.storageSize=1Gi \
  | kubectl apply -f - >/dev/null
kubectl wait -n "${NS}" --for=condition=Ready \
  "clusters.postgresql.cnpg.io/${PG_CLUSTER}" --timeout=5m >/dev/null
restored_pg_pod=$(wait_for_primary 180) \
  || fail "fresh target PostgreSQL primary did not become Ready"
new_cluster_uid=$(kubectl get "clusters.postgresql.cnpg.io/${PG_CLUSTER}" \
  -n "${NS}" -o jsonpath='{.metadata.uid}')
new_pvc_uid=$(kubectl get pvc -n "${NS}" -l "cnpg.io/cluster=${PG_CLUSTER}" \
  -o json | jq -r '.items[0].metadata.uid // empty')
[[ "${new_cluster_uid}" != "${old_cluster_uid}" ]] \
  || fail "fresh Cluster reused the deleted UID"
[[ -n "${new_pvc_uid}" && "${new_pvc_uid}" != "${old_pvc_uid}" ]] \
  || fail "fresh target did not get a new PVC"

restored_password=$(kubectl get secret -n "${NS}" "${PG_CLUSTER}-app" \
  -o jsonpath='{.data.password}' | openssl base64 -d -A)
[[ -n "${restored_password}" && "${restored_password}" != "${source_password}" ]] \
  || fail "fresh target did not rotate the database credential"
assert_sensitive_values_absent "${BACKUP_SQL_FILE}" \
  "${source_password}" "${restored_password}"

step "restore the protected archive with owners, ACLs, and AGE graph objects"
kubectl cp "${BACKUP_FILE}" \
  "${NS}/${restored_pg_pod}:${POD_BACKUP}" -c postgres >/dev/null
if ! kubectl exec -n "${NS}" "${restored_pg_pod}" -c postgres -- \
  pg_restore --clean --if-exists --exit-on-error \
    -U postgres -d kubeatlas "${POD_BACKUP}" 2>"${RESTORE_LOG}"; then
  fail "pg_restore failed; see ${RESTORE_LOG}"
fi
kubectl exec -n "${NS}" "${restored_pg_pod}" -c postgres -- \
  unlink "${POD_BACKUP}"

restored_history=$(sql_value "SELECT count(*) FROM public.resource_events WHERE namespace = '${NS}'")
restored_snapshots=$(sql_value 'SELECT count(*) FROM public.snapshot_meta')
restored_edges=$(sql_value "SELECT count(*) FROM public.edges WHERE from_id = '${NS}/Deployment/recovery-consumer'")
[[ "${restored_history}" == "${history_before}" ]] \
  || fail "history count after restore is ${restored_history}, want ${history_before}"
[[ "${restored_snapshots}" == "${snapshots_before}" ]] \
  || fail "snapshot count after restore is ${restored_snapshots}, want ${snapshots_before}"
[[ "${restored_edges}" == "${edges_before}" ]] \
  || fail "relationship count after restore is ${restored_edges}, want ${edges_before}"
[[ "$(sql_value "SELECT count(*) FROM public.resources WHERE id = '${NS}/ConfigMap/post-backup-only'")" == "0" ]] \
  || fail "post-backup resource was unexpectedly present in the archive"

app_access=$(kubectl exec -n "${NS}" "${restored_pg_pod}" -c postgres -- \
  psql -v ON_ERROR_STOP=1 -U postgres -d kubeatlas -Atc \
    'SET ROLE kubeatlas; SELECT count(*) FROM public.resources')
[[ "${app_access}" =~ [0-9]+$ ]] || fail "application role cannot read restored tables"
age_vertices=$(kubectl exec -n "${NS}" "${restored_pg_pod}" -c postgres -- \
  psql -v ON_ERROR_STOP=1 -U postgres -d kubeatlas -Atc \
    'SET ROLE kubeatlas; SET search_path = ag_catalog, "$user", public; SELECT count(*) FROM cypher('"'"'kubeatlas'"'"', $$ MATCH (n) RETURN n $$) AS (n agtype)' \
  | tail -n 1 | tr -d '[:space:]')
[[ "${age_vertices}" =~ ^[1-9][0-9]*$ ]] \
  || fail "restored AGE graph has no vertices or is inaccessible to the application role"
pass "history, relationships, owners, ACLs, and AGE graph survived restore"

step "start KubeAtlas and require health plus Kubernetes re-sync within 120 seconds"
recovery_started=${SECONDS}
kubectl scale deployment -n "${NS}" "${RELEASE}" --replicas=1 >/dev/null
kubectl rollout status deployment -n "${NS}" "${RELEASE}" --timeout=120s >/dev/null
start_port_forward
deadline=$((SECONDS + 120))
while (( SECONDS < deadline )); do
  current_count=$(sql_value \
    "SELECT count(*) FROM public.resources WHERE id = '${NS}/ConfigMap/post-backup-only'" \
    2>/dev/null || true)
  if [[ "${current_count}" == "1" ]] \
    && api_get '/readyz' >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
[[ "${current_count:-}" == "1" ]] \
  || fail "post-backup Kubernetes state did not re-sync"
recovery_elapsed=$((SECONDS - recovery_started))
(( recovery_elapsed <= 120 )) \
  || fail "KubeAtlas recovery took ${recovery_elapsed}s, budget is 120s"
pass "KubeAtlas healthy and current graph re-synced in ${recovery_elapsed}s"

step "scan restored database, APIs, history, diagnostics, exports, rules, logs, and telemetry"
kubectl exec -n "${NS}" "$(primary_pod)" -c postgres -- \
  pg_dump -Fp -U postgres -d kubeatlas >"${RESTORED_SQL_FILE}"
assert_sensitive_values_absent "${RESTORED_SQL_FILE}" \
  "${source_password}" "${restored_password}"
collect_security_surfaces
assert_sensitive_values_absent "${SURFACES_FILE}" \
  "${source_password}" "${restored_password}"

secret_response=$(api_get "/api/v1/resources/${NS}/Secret/recovery-sentinel")
jq -e --arg namespace "${NS}" '
  .resource == {
    kind: "Secret",
    name: "recovery-sentinel",
    namespace: $namespace,
    annotations: {"kubeatlas.io/reference-only": "true"}
  }
  and (.incoming | length >= 1)
  and (.outgoing | length == 0)
' <<<"${secret_response}" >/dev/null \
  || fail "restored Secret API node is not reference-only"
pass "Secret values and runtime credentials are absent from every gated surface"

printf 'upgrade/recovery evidence: public=%s candidate=%s postgres=%s\n' \
  "${PUBLIC_VERSION}" "${CANDIDATE_IMAGE}" "${CANDIDATE_PG_IMAGE}"
printf 'upgrade/recovery evidence: old-cluster=%s new-cluster=%s recovery=%ss\n' \
  "${old_cluster_uid}" "${new_cluster_uid}" "${recovery_elapsed}"
printf 'upgrade/recovery evidence: history=%s snapshots=%s edges=%s age-vertices=%s\n' \
  "${restored_history}" "${restored_snapshots}" "${restored_edges}" "${age_vertices}"

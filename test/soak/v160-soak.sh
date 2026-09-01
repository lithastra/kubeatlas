#!/usr/bin/env bash

# Run the final v1.6 Docker Desktop Kubernetes endurance gate. This script is
# intentionally strict: it only accepts a clean, frozen commit that already
# has the complete three-row performance evidence set, it refuses short runs,
# and it never retains the random Secret sentinel value.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "${ROOT_DIR}"
source test/soak/lib/v160-soak-event.sh

NAMESPACE="${KUBEATLAS_NAMESPACE:-kubeatlas}"
RELEASE="${KUBEATLAS_RELEASE:-kubeatlas}"
PG_CLUSTER="${KUBEATLAS_PG_CLUSTER:-${RELEASE}-pg}"
PF_PORT="${KUBEATLAS_PF_PORT:-18085}"
EXPECTED_GIT_SHA="${KUBEATLAS_EXPECTED_GIT_SHA:-}"
PERF_DIR="${KUBEATLAS_PERFORMANCE_EVIDENCE_DIR:-}"
OUTPUT_DIR="${KUBEATLAS_SOAK_EVIDENCE_DIR:-/tmp/kubeatlas-v160-soak}"
DURATION_SECONDS="${KUBEATLAS_SOAK_DURATION_SECONDS:-604800}"
WARMUP_SECONDS=86400
BASELINE_SECONDS=86400
SAMPLE_INTERVAL_SECONDS="${KUBEATLAS_SOAK_SAMPLE_INTERVAL_SECONDS:-300}"
FULL_SCAN_INTERVAL_SECONDS=21600
RECOVERY_NAMESPACE="${KUBEATLAS_RECOVERY_NAMESPACE:-kubeatlas-soak-recovery}"
CANDIDATE_IMAGE="${KUBEATLAS_CANDIDATE_IMAGE:-}"
CANDIDATE_PG_IMAGE="${KUBEATLAS_CANDIDATE_PG_IMAGE:-}"
TELEMETRYGEN_IMAGE="${KUBEATLAS_TELEMETRYGEN_IMAGE:-}"
PF_PID=""
OTEL_PF_PID=""
SENTINEL=""
SENTINEL_SHA=""
SENTINEL_SCAN_COUNT=0
SENTINEL_CREATED=0
CANARY_CREATED=0
RUN_PASSED=0
RECOVERY_NAMESPACE_OWNED=0
SURFACE_FILE=""
LAST_FULL_SCAN_AT=0
FORCE_FULL_SCAN=0

SAMPLES_FILE="${OUTPUT_DIR}/samples.jsonl"
EVENTS_FILE="${OUTPUT_DIR}/events.jsonl"
LOG_DIR="${OUTPUT_DIR}/logs"

fail() { printf 'v1.6 soak: %s\n' "$*" >&2; exit 1; }
step() { printf '==> %s\n' "$*"; }
pass() { printf 'PASS: %s\n' "$*"; }

stop_process() {
  local pid=$1
  if [[ -n "${pid}" ]]; then
    if kill -0 "${pid}" 2>/dev/null; then
      kill "${pid}" 2>/dev/null || true
    fi
    wait "${pid}" 2>/dev/null || true
  fi
}

sanitize_file() {
  local path=$1
  local sanitized
  [[ -n "${SENTINEL}" && -f "${path}" ]] || return 0
  grep -Fq -- "${SENTINEL}" "${path}" || return 0
  sanitized="${path}.sanitized"
  sed "s/${SENTINEL}/[REDACTED]/g" "${path}" >"${sanitized}"
  mv "${sanitized}" "${path}"
}

cleanup() {
  local status=$?
  stop_process "${OTEL_PF_PID}"
  stop_process "${PF_PID}"
  if [[ -n "${SURFACE_FILE}" && -e "${SURFACE_FILE}" ]]; then
    unlink "${SURFACE_FILE}"
  fi
  if [[ -d "${OUTPUT_DIR}" ]]; then
    while IFS= read -r evidence_file; do
      sanitize_file "${evidence_file}"
    done < <(find "${OUTPUT_DIR}" -type f 2>/dev/null || true)
  fi
  if (( SENTINEL_CREATED == 1 )); then
    kubectl delete secret -n "${NAMESPACE}" v160-soak-sentinel \
      --ignore-not-found >/dev/null 2>&1 || true
  fi
  if (( CANARY_CREATED == 1 )); then
    kubectl delete configmap -n "${NAMESPACE}" v160-soak-canary \
      --ignore-not-found >/dev/null 2>&1 || true
  fi
  if (( RECOVERY_NAMESPACE_OWNED == 1 )); then
    kubectl delete namespace "${RECOVERY_NAMESPACE}" --ignore-not-found \
      --wait=false >/dev/null 2>&1 || true
  fi
  if (( status != 0 || RUN_PASSED == 0 )); then
    printf 'v1.6 soak stopped without pass evidence; partial sanitized artifacts remain in %s\n' \
      "${OUTPUT_DIR}" >&2
  fi
  return "${status}"
}
trap cleanup EXIT

for command_name in kubectl curl jq git awk sort sed grep openssl helm docker find date; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "missing required command: ${command_name}"
done
if command -v sha256sum >/dev/null 2>&1; then
  sha256_file() { sha256sum "$1" | awk '{print $1}'; }
  sha256_value() { sha256sum | awk '{print $1}'; }
  sha256_stream() { sha256sum | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256_file() { shasum -a 256 "$1" | awk '{print $1}'; }
  sha256_value() { shasum -a 256 | awk '{print $1}'; }
  sha256_stream() { shasum -a 256 | awk '{print $1}'; }
else
  fail "missing required SHA-256 command: sha256sum or shasum"
fi

[[ "${KUBEATLAS_CONFIRM_168H_SOAK:-}" == "docker-desktop" ]] \
  || fail "set KUBEATLAS_CONFIRM_168H_SOAK=docker-desktop to start the disruptive seven-day gate"
[[ "$(kubectl config current-context)" == "docker-desktop" ]] \
  || fail "current Kubernetes context must be docker-desktop"
[[ "${DURATION_SECONDS}" =~ ^[0-9]+$ ]] && (( DURATION_SECONDS >= 604800 )) \
  || fail "the release soak cannot be shorter than 604800 seconds"
[[ "${SAMPLE_INTERVAL_SECONDS}" =~ ^[0-9]+$ ]] \
  && (( SAMPLE_INTERVAL_SECONDS >= 60 && SAMPLE_INTERVAL_SECONDS <= 300 )) \
  || fail "sample interval must be between 60 and 300 seconds"
[[ "${EXPECTED_GIT_SHA}" =~ ^[0-9a-f]{40}$ ]] \
  || fail "KUBEATLAS_EXPECTED_GIT_SHA must be the frozen 40-character commit"
[[ "$(git rev-parse HEAD)" == "${EXPECTED_GIT_SHA}" ]] \
  || fail "HEAD does not match KUBEATLAS_EXPECTED_GIT_SHA"
[[ -z "$(git status --porcelain --untracked-files=normal)" ]] \
  || fail "worktree must be clean for the release soak"
[[ "${RECOVERY_NAMESPACE}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] \
  || fail "KUBEATLAS_RECOVERY_NAMESPACE must be a DNS-label namespace"
[[ "${RECOVERY_NAMESPACE}" != "${NAMESPACE}" && "${RECOVERY_NAMESPACE}" != "default" \
  && "${RECOVERY_NAMESPACE}" != "kube-system" && "${RECOVERY_NAMESPACE}" != "cnpg-system" ]] \
  || fail "recovery namespace must be a dedicated disposable namespace"
[[ -n "${PERF_DIR}" ]] || fail "KUBEATLAS_PERFORMANCE_EVIDENCE_DIR is required"
bash test/verify/v160-performance-evidence.sh \
  "${PERF_DIR}/default-5k-single-large-namespace.json" \
  "${PERF_DIR}/production-10k-distributed.json" \
  "${PERF_DIR}/production-10k-single-large-namespace.json"
perf_sha=$(jq -r '.candidate.git_sha' "${PERF_DIR}/production-10k-distributed.json")
[[ "${perf_sha}" == "${EXPECTED_GIT_SHA}" ]] \
  || fail "performance evidence belongs to ${perf_sha}, not ${EXPECTED_GIT_SHA}"
SOAK_FIXTURE_NAMESPACES=$(jq -r '.fixture.namespaces_csv' "${PERF_DIR}/production-10k-distributed.json")
SOAK_TARGET_NAMESPACE=$(jq -r '.fixture.target_namespace' "${PERF_DIR}/production-10k-distributed.json")
[[ -n "${CANDIDATE_IMAGE}" && -n "${CANDIDATE_PG_IMAGE}" ]] \
  || fail "KUBEATLAS_CANDIDATE_IMAGE and KUBEATLAS_CANDIDATE_PG_IMAGE are required for the final recovery drill"
[[ ! -e "${OUTPUT_DIR}" ]] || fail "refusing to overwrite evidence directory: ${OUTPUT_DIR}"
[[ -z "$(kubectl get secret -n "${NAMESPACE}" v160-soak-sentinel --ignore-not-found -o name)" ]] \
  || fail "Secret ${NAMESPACE}/v160-soak-sentinel already exists"
[[ -z "$(kubectl get namespace "${RECOVERY_NAMESPACE}" --ignore-not-found -o name)" ]] \
  || fail "recovery namespace ${RECOVERY_NAMESPACE} already exists"

mkdir -p "${LOG_DIR}"
: >"${SAMPLES_FILE}"
: >"${EVENTS_FILE}"

start_port_forward() {
  local target=${1:-deployment/${RELEASE}}
  local timeout_seconds=${2:-60}
  local deadline=$((SECONDS + timeout_seconds))
  local attempts=0

  stop_process "${PF_PID}"
  PF_PID=""
  while (( SECONDS < deadline )); do
    if [[ -z "${PF_PID}" ]] || ! kill -0 "${PF_PID}" 2>/dev/null; then
      stop_process "${PF_PID}"
      attempts=$((attempts + 1))
      printf 'attempt=%s target=%s\n' "${attempts}" "${target}" \
        >>"${LOG_DIR}/port-forward.log"
      kubectl port-forward -n "${NAMESPACE}" "${target}" \
        "${PF_PORT}:8080" >>"${LOG_DIR}/port-forward.log" 2>&1 &
      PF_PID=$!
    fi
    if kill -0 "${PF_PID}" 2>/dev/null \
      && curl -fsS --max-time 1 "http://127.0.0.1:${PF_PORT}/readyz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  stop_process "${PF_PID}"
  PF_PID=""
  fail "KubeAtlas did not become reachable through ${target} port-forward after ${attempts} attempts"
}

api() { curl -fsS --max-time 10 "http://127.0.0.1:${PF_PORT}$1"; }
append_security_surface() {
  local endpoint=$1 allow_not_found=${2:-false} response http_code response_body
  if ! response=$(curl -sS --max-time 10 -w $'\n%{http_code}' \
    "http://127.0.0.1:${PF_PORT}${endpoint}"); then
    fail "security surface ${endpoint} was unreachable"
  fi
  http_code=${response##*$'\n'}
  response_body=${response%$'\n'*}
  printf '%s\n' "${response_body}" >>"${SURFACE_FILE}"
  if [[ "${http_code}" == "200" ]]; then
    return 0
  fi
  # An unreferenced Secret has no reference-only graph placeholder, so its
  # detail endpoint correctly returns 404. Retain and scan that bounded error
  # body, while rejecting 404s from every other surface and all other errors.
  if [[ "${allow_not_found}" == "true" && "${http_code}" == "404" ]]; then
    return 0
  fi
  fail "security surface ${endpoint} returned HTTP ${http_code}"
}
metric() {
  local metrics=$1 name=$2
  awk -v name="${name}" '$1 == name {print $2; found=1} END {if (!found) print 0}' <<<"${metrics}" | tail -n1
}
metric_label() {
  local metrics=$1 line=$2
  awk -v line="${line}" '$1 == line {print $2; found=1} END {if (!found) print 0}' <<<"${metrics}" | tail -n1
}

assert_sentinel_absent_file() {
  local path=$1 description=$2
  SENTINEL_SCAN_COUNT=$((SENTINEL_SCAN_COUNT + 1))
  if grep -Fq -- "${SENTINEL}" "${path}"; then
    fail "Secret sentinel reached ${description}"
  fi
}

scan_security_surfaces() {
  local now full_scan=0 primary
  now=$(date +%s)
  if (( FORCE_FULL_SCAN == 1 || LAST_FULL_SCAN_AT == 0 || now - LAST_FULL_SCAN_AT >= FULL_SCAN_INTERVAL_SECONDS )); then
    full_scan=1
  fi
  SURFACE_FILE=$(mktemp)
  : >"${SURFACE_FILE}"
  append_security_surface \
    "/api/v1/resources/${NAMESPACE}/Secret/v160-soak-sentinel" true
  for endpoint in \
    '/api/v1/graph?level=cluster' \
    '/api/v1/snapshots' \
    "/api/v1/diagnose?namespace=${NAMESPACE}" \
    "/api/v1alpha1/export?format=svg&namespace=${NAMESPACE}" \
    '/api/v1/policy/constraints' \
    '/api/v1/telemetry/status' \
    '/api/v1/telemetry/preview' \
    '/metrics'; do
    append_security_surface "${endpoint}"
  done
  if (( full_scan == 1 )); then
    kubectl logs -n "${NAMESPACE}" -l "app.kubernetes.io/instance=${RELEASE}" \
      --all-containers=true --tail=5000 >>"${SURFACE_FILE}" 2>&1
    kubectl logs -n "${NAMESPACE}" -l "cnpg.io/cluster=${PG_CLUSTER}" \
      --all-containers=true --tail=5000 >>"${SURFACE_FILE}" 2>&1
    primary=$(kubectl get pods -n "${NAMESPACE}" \
      -l "cnpg.io/cluster=${PG_CLUSTER},cnpg.io/instanceRole=primary" \
      -o jsonpath='{.items[0].metadata.name}')
    kubectl exec -n "${NAMESPACE}" "${primary}" -c postgres -- \
      pg_dump -Fp --no-owner --no-privileges -U postgres -d kubeatlas \
      >>"${SURFACE_FILE}" 2>&1
    verify_fixture_counts
    LAST_FULL_SCAN_AT=${now}
    FORCE_FULL_SCAN=0
  fi
  assert_sentinel_absent_file "${SURFACE_FILE}" "live APIs, logs, and PostgreSQL"
  unlink "${SURFACE_FILE}"
  SURFACE_FILE=""
}

verify_fixture_counts() {
  local namespace counts='{"configmaps":0,"deployments":0,"services":0}' namespace_counts
  IFS=',' read -r -a namespaces <<<"${SOAK_FIXTURE_NAMESPACES}"
  for namespace in "${namespaces[@]}"; do
    namespace_counts=$(kubectl get configmaps,deployments.apps,services -n "${namespace}" -o json \
      | jq -c '
          reduce .items[] as $item ({configmaps:0,deployments:0,services:0};
            if $item.kind == "ConfigMap" and ($item.metadata.name | test("^cm-[0-9]{5}$")) then .configmaps += 1
            elif $item.kind == "Deployment" and ($item.metadata.name | test("^dep-[0-9]{4}$")) then .deployments += 1
            elif $item.kind == "Service" and ($item.metadata.name | test("^svc-[0-9]{4}$")) then .services += 1
            else . end)')
    counts=$(jq -cn --argjson left "${counts}" --argjson right "${namespace_counts}" \
      '{configmaps:($left.configmaps+$right.configmaps),deployments:($left.deployments+$right.deployments),services:($left.services+$right.services)}')
  done
  jq -e '.configmaps == 10000 and .deployments == 2000 and .services == 400' \
    <<<"${counts}" >/dev/null || fail "distributed 10K fixture drifted during the soak"
}

latency_sample() {
  local name=$1 url=$2 body elapsed failure=false
  body=$(mktemp)
  if ! elapsed=$(curl -fsS --max-time 10 -o "${body}" -w '%{time_total}' "${url}"); then
    elapsed=99
    failure=true
  fi
  assert_sentinel_absent_file "${body}" "${name} response"
  unlink "${body}"
  jq -cn --argjson failure "${failure}" --arg elapsed "${elapsed}" \
    '{failure: $failure, latency_ms: (($elapsed | tonumber) * 1000)}'
}

current_app_pod() {
  kubectl get pods -n "${NAMESPACE}" \
    -l "app.kubernetes.io/name=kubeatlas,app.kubernetes.io/instance=${RELEASE}" \
    -o json | jq -c '[.items[] | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | sort_by(.metadata.creationTimestamp) | last'
}

wait_for_replacement_app_pod() {
  local old_uid=$1 timeout_seconds=$2
  local deadline=$((SECONDS + timeout_seconds))
  local pod_json

  while (( SECONDS < deadline )); do
    pod_json=$(kubectl get pods -n "${NAMESPACE}" \
      -l "app.kubernetes.io/name=kubeatlas,app.kubernetes.io/instance=${RELEASE}" \
      -o json | jq -c --arg old_uid "${old_uid}" '
        [.items[]
          | select(.metadata.uid != $old_uid)
          | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))]
        | sort_by(.metadata.creationTimestamp)
        | last // empty')
    if [[ -n "${pod_json}" ]]; then
      printf '%s\n' "${pod_json}"
      return 0
    fi
    sleep 1
  done
  return 1
}

record_event() {
  v160_soak_event_json "$@" >>"${EVENTS_FILE}"
}

run_logged() {
  local log_file=$1
  shift
  "$@" >"${log_file}" 2>&1
  assert_sentinel_absent_file "${log_file}" "event log ${log_file##*/}"
}

SENTINEL=$(openssl rand -hex 32)
SENTINEL_SHA=$(printf '%s' "${SENTINEL}" | sha256_value)
kubectl create secret generic v160-soak-sentinel -n "${NAMESPACE}" \
  --from-literal=token="${SENTINEL}" >/dev/null
SENTINEL_CREATED=1
kubectl create configmap v160-soak-canary -n "${NAMESPACE}" \
  --from-literal=sequence=0 >/dev/null
CANARY_CREATED=1
start_port_forward

initial_pod_json=$(current_app_pod)
initial_pod_uid=$(jq -r '.metadata.uid // empty' <<<"${initial_pod_json}")
initial_app_image_id=$(jq -r '.status.containerStatuses[] | select(.name == "kubeatlas") | .imageID' <<<"${initial_pod_json}")
pg_pod_json=$(kubectl get pods -n "${NAMESPACE}" -l "cnpg.io/cluster=${PG_CLUSTER}" -o json \
  | jq -c '[.items[] | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | last')
initial_pg_image_id=$(jq -r '.status.containerStatuses[] | select(.name == "postgres") | .imageID' <<<"${pg_pod_json}")
[[ "${initial_app_image_id}" == *@sha256:* && "${initial_pg_image_id}" == *@sha256:* ]] \
  || fail "application and PostgreSQL must run immutable image IDs"
expected_app_image_id=$(jq -r '.candidate.app_image_id' "${PERF_DIR}/production-10k-distributed.json")
expected_pg_image_id=$(jq -r '.candidate.postgres_image_id' "${PERF_DIR}/production-10k-distributed.json")
[[ "${initial_app_image_id}" == "${expected_app_image_id}" && "${initial_pg_image_id}" == "${expected_pg_image_id}" ]] \
  || fail "running image IDs do not match the performance-gated candidate"
expected_manifest_sha=$(jq -r '.candidate.chart_manifest_sha256' "${PERF_DIR}/production-10k-distributed.json")
running_manifest_sha=$(helm get manifest "${RELEASE}" -n "${NAMESPACE}" | sha256_stream)
[[ "${running_manifest_sha}" == "${expected_manifest_sha}" ]] \
  || fail "running Helm manifest does not match distributed 10K performance evidence"
app_resources=$(kubectl get deployment -n "${NAMESPACE}" "${RELEASE}" -o json \
  | jq -c '.spec.template.spec.containers[] | select(.name == "kubeatlas") | .resources')
pg_resources=$(kubectl get "clusters.postgresql.cnpg.io/${PG_CLUSTER}" -n "${NAMESPACE}" -o json \
  | jq -c '.spec.resources // {}')
jq -e '.requests == {cpu:"500m",memory:"512Mi"} and .limits == {cpu:"2",memory:"2Gi"}' \
  <<<"${app_resources}" >/dev/null || fail "soak application resources are not the production 10K profile"
jq -e '.requests == {cpu:"500m",memory:"1Gi"} and .limits == {cpu:"2",memory:"2Gi"}' \
  <<<"${pg_resources}" >/dev/null || fail "soak PostgreSQL resources are not the production 10K profile"
deployed_image=$(kubectl get deployment -n "${NAMESPACE}" "${RELEASE}" \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="kubeatlas")].image}')
deployed_pg_image=$(kubectl get "clusters.postgresql.cnpg.io/${PG_CLUSTER}" -n "${NAMESPACE}" \
  -o jsonpath='{.spec.imageName}')
[[ "${deployed_image}" == "${CANDIDATE_IMAGE}" && "${deployed_pg_image}" == "${CANDIDATE_PG_IMAGE}" ]] \
  || fail "recovery-drill candidate image tags do not match the running soak deployment"
verify_fixture_counts

initial_metrics=$(api /metrics)
grep -Fq 'kubeatlas_storage_durable 1' <<<"${initial_metrics}" || fail "Tier 2 durable storage is required"
grep -Fq 'kubeatlas_snapshot_queue_depth ' <<<"${initial_metrics}" || fail "snapshots must be enabled"
otel_enabled=false
grep -q '^kubeatlas_otel_received_total ' <<<"${initial_metrics}" && otel_enabled=true
if [[ "${otel_enabled}" == "true" ]]; then
  [[ "${TELEMETRYGEN_IMAGE}" =~ @sha256:[0-9a-f]{64}$ ]] \
    || fail "OTel-enabled release evidence requires KUBEATLAS_TELEMETRYGEN_IMAGE pinned by digest"
fi
previous_queue_drop=$(metric "${initial_metrics}" kubeatlas_snapshot_queue_drop_total)
previous_write_failed=$(metric "${initial_metrics}" kubeatlas_snapshot_write_failed_total)
previous_otel_dropped=$(metric "${initial_metrics}" kubeatlas_otel_dropped_total)

sample_sequence=0
next_load_class=normal
capture_sample() {
  local now elapsed phase metrics pod_json pod_name pod_uid restart_count oom rss_kib
  local cluster_result namespace_result blast_result queue_drop write_failed otel_dropped
  now=$(date +%s)
  elapsed=$((now - soak_started_at))
  if (( elapsed < WARMUP_SECONDS )); then phase=warmup
  elif (( elapsed < WARMUP_SECONDS + BASELINE_SECONDS )); then phase=baseline
  else phase=steady
  fi
  [[ "${next_load_class}" == "normal" ]] || phase=event

  kubectl create configmap v160-soak-canary -n "${NAMESPACE}" \
    --from-literal="sequence=${sample_sequence}" --dry-run=client -o yaml \
    | kubectl apply -f - >/dev/null
  metrics=$(api /metrics)
  pod_json=$(current_app_pod)
  pod_name=$(jq -r '.metadata.name // empty' <<<"${pod_json}")
  pod_uid=$(jq -r '.metadata.uid // empty' <<<"${pod_json}")
  restart_count=$(jq -r '.status.containerStatuses[] | select(.name == "kubeatlas") | .restartCount' <<<"${pod_json}")
  oom=$(jq -r '[.status.containerStatuses[] | select(.name == "kubeatlas") | .lastState.terminated.reason == "OOMKilled"] | any' <<<"${pod_json}")
  rss_kib=$(kubectl exec -n "${NAMESPACE}" "${pod_name}" -c kubeatlas -- \
    cat /proc/1/status | awk '$1 == "VmRSS:" {print $2; exit}')
  cluster_result=$(latency_sample cluster "http://127.0.0.1:${PF_PORT}/api/v1/graph?level=cluster")
  namespace_result=$(latency_sample namespace "http://127.0.0.1:${PF_PORT}/api/v1/graph?level=namespace&namespace=${SOAK_TARGET_NAMESPACE}")
  blast_result=$(latency_sample blast-radius "http://127.0.0.1:${PF_PORT}/api/v1/blast-radius/${SOAK_TARGET_NAMESPACE}/ConfigMap/cm-00000")
  queue_drop=$(metric "${metrics}" kubeatlas_snapshot_queue_drop_total)
  write_failed=$(metric "${metrics}" kubeatlas_snapshot_write_failed_total)
  otel_dropped=$(metric "${metrics}" kubeatlas_otel_dropped_total)
  scan_security_surfaces

  jq -cn \
    --arg schema 'https://kubeatlas.lithastra.com/schemas/v160-soak-sample-v1.json' \
    --arg phase "${phase}" --arg load_class "${next_load_class}" --arg pod_uid "${pod_uid}" \
    --argjson captured_at_epoch "${now}" --argjson rss_bytes "$((rss_kib * 1024))" \
    --argjson goroutines "$(metric "${metrics}" kubeatlas_goroutines)" \
    --argjson queue_depth "$(metric "${metrics}" kubeatlas_snapshot_queue_depth)" \
    --argjson restart_count "${restart_count}" --argjson oom_killed "${oom}" \
    --argjson ready "$(curl -fsS --max-time 5 "http://127.0.0.1:${PF_PORT}/readyz" >/dev/null 2>&1 && printf true || printf false)" \
    --argjson api_reachable "$(metric "${metrics}" kubeatlas_kubernetes_api_reachable)" \
    --argjson storage_reachable "$(metric "${metrics}" kubeatlas_storage_reachable)" \
    --argjson graph_synced "$(metric_label "${metrics}" 'kubeatlas_graph_observation_state{state="synced"}')" \
    --argjson cluster "${cluster_result}" --argjson namespace_result "${namespace_result}" --argjson blast "${blast_result}" \
    --argjson queue_drop_delta "$((queue_drop - previous_queue_drop))" \
    --argjson write_failed_delta "$((write_failed - previous_write_failed))" \
    --argjson otel_dropped_delta "$((otel_dropped - previous_otel_dropped))" '
    {
      "$schema": $schema, captured_at_epoch: $captured_at_epoch, phase: $phase, load_class: $load_class,
      process: {rss_bytes: $rss_bytes, goroutines: $goroutines, queue_depth: $queue_depth, pod_uid: $pod_uid, restart_count: $restart_count, oom_killed: $oom_killed},
      health: {ready: $ready, kubernetes_api_reachable: ($api_reachable == 1), storage_reachable: ($storage_reachable == 1), graph_synced: ($graph_synced == 1)},
      endpoints: {cluster: $cluster, namespace: $namespace_result, blast_radius: $blast},
      counter_deltas: {snapshot_queue_drop: $queue_drop_delta, snapshot_write_failed: $write_failed_delta, otel_dropped: $otel_dropped_delta},
      sentinel_absent: true
    }' >>"${SAMPLES_FILE}"

  if [[ "${next_load_class}" == "normal" ]]; then
    (( restart_count == 0 )) || fail "unexpected application container restart"
    [[ "${oom}" == "false" ]] || fail "application was OOMKilled"
    (( queue_drop == previous_queue_drop )) || fail "snapshot work dropped during normal load"
    (( write_failed == previous_write_failed )) || fail "snapshot write failed during normal load"
    (( otel_dropped == previous_otel_dropped )) || fail "OTel work dropped during normal load"
    jq -e '.failure == false' <<<"${cluster_result}" >/dev/null || fail "cluster query failed"
    jq -e '.failure == false' <<<"${namespace_result}" >/dev/null || fail "namespace query failed"
    jq -e '.failure == false' <<<"${blast_result}" >/dev/null || fail "blast-radius query failed"
    grep -Fq 'kubeatlas_kubernetes_api_reachable 1' <<<"${metrics}" || fail "Kubernetes API unhealthy outside an event"
    grep -Fq 'kubeatlas_storage_reachable 1' <<<"${metrics}" || fail "PostgreSQL unhealthy outside an event"
    grep -Fq 'kubeatlas_graph_observation_state{state="synced"} 1' <<<"${metrics}" || fail "graph unsynced outside an event"
  fi
  previous_queue_drop=${queue_drop}
  previous_write_failed=${write_failed}
  previous_otel_dropped=${otel_dropped}
  next_load_class=normal
  sample_sequence=$((sample_sequence + 1))
}

event_app_restart() {
  local before_json before_name before_uid started restart_deadline remaining
  local after_json after_name after_uid recovery
  before_json=$(current_app_pod)
  before_name=$(jq -r '.metadata.name // empty' <<<"${before_json}")
  before_uid=$(jq -r '.metadata.uid // empty' <<<"${before_json}")
  [[ -n "${before_name}" && -n "${before_uid}" ]] \
    || fail "app restart could not identify the current Ready Pod"
  started=$(date +%s)
  restart_deadline=$((SECONDS + 120))
  stop_process "${PF_PID}"
  PF_PID=""
  kubectl delete pod -n "${NAMESPACE}" "${before_name}" \
    --wait=false >"${LOG_DIR}/app-restart.log" 2>&1
  remaining=$((restart_deadline - SECONDS))
  (( remaining > 0 )) || fail "app restart recovery exceeded 120 seconds before replacement"
  after_json=$(wait_for_replacement_app_pod "${before_uid}" "${remaining}") \
    || fail "app restart did not produce a replacement Ready Pod within 120 seconds"
  after_name=$(jq -r '.metadata.name // empty' <<<"${after_json}")
  after_uid=$(jq -r '.metadata.uid // empty' <<<"${after_json}")
  [[ -n "${after_name}" && -n "${after_uid}" && "${after_uid}" != "${before_uid}" ]] \
    || fail "app restart did not replace the Pod"
  remaining=$((restart_deadline - SECONDS))
  (( remaining > 0 )) || fail "app restart recovery exceeded 120 seconds before port-forward"
  start_port_forward "pod/${after_name}" "${remaining}"
  recovery=$(( $(date +%s) - started ))
  (( recovery <= 120 )) || fail "app restart recovery exceeded 120 seconds"
  restarted_pod_uid=${after_uid}
  assert_sentinel_absent_file "${LOG_DIR}/app-restart.log" "app restart log"
  record_event app-restart pass "${recovery}" "$(jq -cn --arg before "${before_uid}" --arg after "${after_uid}" '{before_pod_uid:$before,after_pod_uid:$after}')"
}

event_resource_storm() {
  run_logged "${LOG_DIR}/resource-storm.log" env \
    NS=v160-soak-resource-storm COUNT=100 KUBEATLAS_URL="http://127.0.0.1:${PF_PORT}" \
    bash test/chaos/resource-storm.sh
  record_event resource-storm pass 0
}

event_snapshot_storm() {
  run_logged "${LOG_DIR}/snapshot-write-storm.log" env \
    NS=v160-soak-snapshot-storm KUBEATLAS_PF_PORT="${PF_PORT}" \
    KUBEATLAS_NAMESPACE="${NAMESPACE}" KUBEATLAS_REQUIRE_TIER2=1 \
    bash test/chaos/snapshot-write-storm.sh
  record_event snapshot-write-storm pass 0
}

event_postgresql_interruption() {
  local result="${OUTPUT_DIR}/.postgresql-result.json"
  run_logged "${LOG_DIR}/postgresql-interruption.log" env \
    NS="${NAMESPACE}" RELEASE="${RELEASE}" KUBEATLAS_PF_PORT="${PF_PORT}" \
    KUBEATLAS_CHAOS_RESULT_FILE="${result}" bash test/chaos/pg-disconnect.sh
  record_event postgresql-interruption pass "$(jq -r '.recovery_seconds' "${result}")" "$(jq -c . "${result}")"
  unlink "${result}"
}

event_api_interruption() {
  local result="${OUTPUT_DIR}/.api-result.json"
  run_logged "${LOG_DIR}/api-server-interruption.log" env \
    KUBEATLAS_NAMESPACE="${NAMESPACE}" KUBEATLAS_RELEASE="${RELEASE}" \
    KUBEATLAS_CONFIRM_API_FLAP=docker-desktop KUBEATLAS_CHAOS_RESULT_FILE="${result}" \
    bash test/chaos/api-server-flap.sh
  start_port_forward
  record_event api-server-interruption pass "$(jq -r '.recovery_seconds' "${result}")" "$(jq -c . "${result}")"
  unlink "${result}"
}

event_otel_overload() {
  if [[ "${otel_enabled}" != "true" ]]; then
    record_event otel-overload not-applicable 0 '{"reason":"otel-disabled"}'
    return
  fi
  kubectl port-forward -n "${NAMESPACE}" "deployment/${RELEASE}" 4317:4317 \
    >"${LOG_DIR}/otel-port-forward.log" 2>&1 &
  OTEL_PF_PID=$!
  sleep 3
  run_logged "${LOG_DIR}/otel-overload.log" env \
    KUBEATLAS_PF_PORT="${PF_PORT}" NAMESPACE="${NAMESPACE}" \
    TELEMETRYGEN_IMAGE="${TELEMETRYGEN_IMAGE}" \
    bash test/chaos/otel-receiver-overload.sh
  stop_process "${OTEL_PF_PID}"
  OTEL_PF_PID=""
  record_event otel-overload pass 0
}

soak_started_at=$(date +%s)
next_sample_at=${soak_started_at}
finished_at_epoch=0
restarted_pod_uid=""
app_restart_done=0
resource_storm_done=0
snapshot_storm_done=0
pg_interruption_done=0
api_interruption_done=0
otel_overload_done=0

step "starting ${DURATION_SECONDS}-second soak for ${EXPECTED_GIT_SHA}"
while (( $(date +%s) - soak_started_at < DURATION_SECONDS )); do
  elapsed=$(( $(date +%s) - soak_started_at ))
  if (( app_restart_done == 0 && elapsed >= 172800 )); then
    event_app_restart; app_restart_done=1; next_load_class=intentional-overload; FORCE_FULL_SCAN=1
  elif (( resource_storm_done == 0 && elapsed >= 259200 )); then
    event_resource_storm; resource_storm_done=1; next_load_class=intentional-overload; FORCE_FULL_SCAN=1
  elif (( snapshot_storm_done == 0 && elapsed >= 345600 )); then
    event_snapshot_storm; snapshot_storm_done=1; next_load_class=intentional-overload; FORCE_FULL_SCAN=1
  elif (( pg_interruption_done == 0 && elapsed >= 432000 )); then
    event_postgresql_interruption; pg_interruption_done=1; next_load_class=intentional-overload; FORCE_FULL_SCAN=1
  elif (( api_interruption_done == 0 && elapsed >= 518400 )); then
    event_api_interruption; api_interruption_done=1; next_load_class=intentional-overload; FORCE_FULL_SCAN=1
  elif (( otel_overload_done == 0 && elapsed >= 561600 )); then
    event_otel_overload; otel_overload_done=1; next_load_class=intentional-overload; FORCE_FULL_SCAN=1
  fi
  capture_sample
  next_sample_at=$((next_sample_at + SAMPLE_INTERVAL_SECONDS))
  now=$(date +%s)
  while (( next_sample_at <= now )); do
    next_sample_at=$((next_sample_at + SAMPLE_INTERVAL_SECONDS))
  done
  remaining=$((DURATION_SECONDS - (now - soak_started_at)))
  (( remaining > 0 )) || break
  sleep_for=$((next_sample_at - now))
  (( remaining >= sleep_for )) || sleep_for=${remaining}
  sleep "${sleep_for}"
done
finished_at_epoch=$(date +%s)

for event_flag in app_restart_done resource_storm_done snapshot_storm_done pg_interruption_done api_interruption_done otel_overload_done; do
  (( ${!event_flag} == 1 )) || fail "scheduled event ${event_flag%_done} did not run"
done

step "running the final public v1.5.2 upgrade, destructive restore, and candidate re-sync drill"
RECOVERY_NAMESPACE_OWNED=1
run_logged "${LOG_DIR}/final-upgrade-restore.log" env \
  KUBEATLAS_NAMESPACE="${RECOVERY_NAMESPACE}" \
  KUBEATLAS_CANDIDATE_IMAGE="${CANDIDATE_IMAGE}" \
  KUBEATLAS_CANDIDATE_PG_IMAGE="${CANDIDATE_PG_IMAGE}" \
  bash test/verify/v160-upgrade-recovery.sh
kubectl delete namespace "${RECOVERY_NAMESPACE}" --wait=true --timeout=10m \
  >>"${LOG_DIR}/final-upgrade-restore.log" 2>&1
RECOVERY_NAMESPACE_OWNED=0
assert_sentinel_absent_file "${LOG_DIR}/final-upgrade-restore.log" "final recovery and cleanup log"
record_event final-upgrade-restore pass 0

[[ -n "${restarted_pod_uid}" ]] || fail "expected replacement Pod UID is empty"
artifacts_file=$(mktemp)
: >"${artifacts_file}"
for artifact_path in "${SAMPLES_FILE}" "${EVENTS_FILE}" "${LOG_DIR}"/*.log; do
  [[ -f "${artifact_path}" ]] || continue
  assert_sentinel_absent_file "${artifact_path}" "retained artifact ${artifact_path##*/}"
  relative_path=${artifact_path#"${OUTPUT_DIR}/"}
  jq -cn --arg path "${relative_path}" --arg sha256 "$(sha256_file "${artifact_path}")" \
    '{path:$path,sha256:$sha256,sentinel_absent:true}' >>"${artifacts_file}"
done
artifacts=$(jq -s . "${artifacts_file}")
unlink "${artifacts_file}"

server_version=$(kubectl version -o json | jq -r '.serverVersion.gitVersion')
jq -n \
  --arg schema 'https://kubeatlas.lithastra.com/schemas/v160-soak-evidence-v1.json' \
  --arg git_sha "${EXPECTED_GIT_SHA}" --arg app_image_id "${initial_app_image_id}" \
  --arg pg_image_id "${initial_pg_image_id}" --arg kubernetes "${server_version}" \
  --arg sentinel_sha "${SENTINEL_SHA}" --arg initial_uid "${initial_pod_uid}" \
  --arg restarted_uid "${restarted_pod_uid}" --argjson artifacts "${artifacts}" \
  --argjson started_at_epoch "${soak_started_at}" --argjson finished_at_epoch "${finished_at_epoch}" \
  --argjson duration_seconds "${DURATION_SECONDS}" --argjson sample_interval "${SAMPLE_INTERVAL_SECONDS}" \
  --argjson otel_enabled "${otel_enabled}" --argjson scan_count "${SENTINEL_SCAN_COUNT}" '
  {
    "$schema": $schema, status: "pass",
    candidate: {git_sha:$git_sha,dirty:false,app_image_id:$app_image_id,postgres_image_id:$pg_image_id},
    environment: {kubernetes_context:"docker-desktop",kubernetes_server_version:$kubernetes},
    configuration: {duration_seconds:$duration_seconds,warmup_seconds:86400,baseline_seconds:86400,sample_interval_seconds:$sample_interval,otel_enabled:$otel_enabled},
    started_at_epoch:$started_at_epoch,finished_at_epoch:$finished_at_epoch,
    sentinel:{sha256:$sentinel_sha,raw_value_retained:false,scan_count:$scan_count},
    expected_app_pod_uids:[$initial_uid,$restarted_uid],artifacts:$artifacts
  }' >"${OUTPUT_DIR}/manifest.json"

bash test/verify/v160-soak-evidence.sh "${OUTPUT_DIR}"
RUN_PASSED=1
pass "v1.6 168-hour soak complete: ${OUTPUT_DIR}"

#!/usr/bin/env bash

# Capture one reproducible v1.6 performance result against an already-loaded
# fixture and fail on the frozen latency/resource contract. Raw API responses
# are never retained; the evidence contains only timings, counts, immutable
# image IDs, resource profiles, and bounded runtime context.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "${ROOT_DIR}"

PROFILE="${KUBEATLAS_PERF_PROFILE:-default-5k}"
LAYOUT="${KUBEATLAS_PERF_LAYOUT:-single-large-namespace}"
NAMESPACES_CSV="${KUBEATLAS_FIXTURE_NAMESPACES:-stress-test-5k}"
TARGET_NAMESPACE="${KUBEATLAS_TARGET_NAMESPACE:-${NAMESPACES_CSV%%,*}}"
RELEASE="${KUBEATLAS_RELEASE:-kubeatlas}"
NAMESPACE="${KUBEATLAS_NAMESPACE:-kubeatlas}"
PG_CLUSTER="${KUBEATLAS_PG_CLUSTER:-${RELEASE}-pg}"
SAMPLES="${KUBEATLAS_PERF_SAMPLES:-100}"
PF_PORT="${KUBEATLAS_PF_PORT:-18084}"
OUTPUT_DIR="${KUBEATLAS_EVIDENCE_DIR:-/tmp/kubeatlas-v160-performance}"
EXPECTED_GIT_SHA="${KUBEATLAS_EXPECTED_GIT_SHA:-}"
ALLOW_DIRTY="${KUBEATLAS_EVIDENCE_ALLOW_DIRTY:-0}"
CLUSTER_P95_LIMIT_MS=1000
NAMESPACE_P95_LIMIT_MS=1000
BLAST_P95_LIMIT_MS=500
PF_PID=""

fail() { printf 'v1.6 performance gate: %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS: %s\n' "$*"; }

cleanup() {
  if [[ -n "${PF_PID}" ]] && kill -0 "${PF_PID}" 2>/dev/null; then
    kill "${PF_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
  fi
  local timing_file timing_name
  for timing_name in cluster namespace blast; do
    timing_file="${OUTPUT_DIR}/.${PROFILE}-${LAYOUT}-${timing_name}.timings"
    [[ ! -e "${timing_file}" ]] || unlink "${timing_file}"
  done
}
trap cleanup EXIT

for command_name in kubectl curl jq git awk sort sed uname helm docker grep; do
  command -v "${command_name}" >/dev/null 2>&1 \
    || fail "missing required command: ${command_name}"
done
if command -v sha256sum >/dev/null 2>&1; then
  sha256_stream() { sha256sum | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256_stream() { shasum -a 256 | awk '{print $1}'; }
else
  fail "missing required SHA-256 command: sha256sum or shasum"
fi

case "${PROFILE}" in
  default-5k|production-10k) ;;
  *) fail "KUBEATLAS_PERF_PROFILE must be default-5k or production-10k" ;;
esac
case "${LAYOUT}" in
  distributed|single-large-namespace) ;;
  *) fail "KUBEATLAS_PERF_LAYOUT must be distributed or single-large-namespace" ;;
esac
[[ "${SAMPLES}" =~ ^[0-9]+$ ]] && (( SAMPLES >= 20 )) \
  || fail "KUBEATLAS_PERF_SAMPLES must be an integer >= 20"
[[ "${NAMESPACES_CSV}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?(,[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$ ]] \
  || fail "KUBEATLAS_FIXTURE_NAMESPACES must be a comma-separated list of DNS-label namespaces"
[[ "${TARGET_NAMESPACE}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] \
  || fail "KUBEATLAS_TARGET_NAMESPACE must be a DNS-label namespace"
[[ "$(kubectl config current-context)" == "docker-desktop" ]] \
  || fail "current Kubernetes context must be docker-desktop"

head_sha=$(git rev-parse HEAD)
[[ "${EXPECTED_GIT_SHA}" =~ ^[0-9a-f]{40}$ ]] \
  || fail "KUBEATLAS_EXPECTED_GIT_SHA must be the frozen 40-character commit"
[[ "${head_sha}" == "${EXPECTED_GIT_SHA}" ]] \
  || fail "HEAD ${head_sha} does not match frozen commit ${EXPECTED_GIT_SHA}"
dirty=false
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  dirty=true
  [[ "${ALLOW_DIRTY}" == "1" ]] \
    || fail "worktree is dirty; performance evidence requires a frozen checkout"
fi

mkdir -p "${OUTPUT_DIR}"
output_file="${OUTPUT_DIR}/${PROFILE}-${LAYOUT}.json"
[[ ! -e "${output_file}" ]] \
  || fail "refusing to overwrite existing evidence: ${output_file}"

kubectl port-forward -n "${NAMESPACE}" "deployment/${RELEASE}" \
  "${PF_PORT}:8080" >"${OUTPUT_DIR}/port-forward.log" 2>&1 &
PF_PID=$!
base_url="http://127.0.0.1:${PF_PORT}"
for _ in $(seq 1 60); do
  if curl -fsS --max-time 1 "${base_url}/readyz" >/dev/null 2>&1; then
    break
  fi
  kill -0 "${PF_PID}" 2>/dev/null \
    || fail "port-forward exited before KubeAtlas became ready"
  sleep 1
done
curl -fsS --max-time 2 "${base_url}/readyz" >/dev/null \
  || fail "KubeAtlas did not become ready on ${base_url}"

metrics=$(curl -fsS --max-time 10 "${base_url}/metrics")
grep -Fq 'kubeatlas_storage_durable 1' <<<"${metrics}" \
  || fail "performance evidence requires Tier 2 durable storage"
grep -Fq 'kubeatlas_storage_reachable 1' <<<"${metrics}" \
  || fail "storage is not healthy before sampling"
grep -Fq 'kubeatlas_graph_observation_state{state="synced"} 1' <<<"${metrics}" \
  || fail "graph is not synced before sampling"
grep -Fq 'kubeatlas_snapshot_queue_depth ' <<<"${metrics}" \
  || fail "snapshots and their queue-depth metric must be enabled"

app_resources=$(kubectl get deployment -n "${NAMESPACE}" "${RELEASE}" -o json \
  | jq -c '.spec.template.spec.containers[] | select(.name == "kubeatlas") | .resources')
pg_resources=$(kubectl get "clusters.postgresql.cnpg.io/${PG_CLUSTER}" \
  -n "${NAMESPACE}" -o json | jq -c '.spec.resources // {}')

if [[ "${PROFILE}" == "default-5k" ]]; then
  jq -e '
    .requests.cpu == "100m" and .requests.memory == "128Mi"
    and .limits.cpu == "500m" and .limits.memory == "512Mi"
  ' <<<"${app_resources}" >/dev/null \
    || fail "default-5k must use the chart's exact default application resources"
else
  jq -e '
    .requests.cpu == "500m" and .requests.memory == "512Mi"
    and .limits.cpu == "2" and .limits.memory == "2Gi"
  ' <<<"${app_resources}" >/dev/null \
    || fail "production-10k application resources do not match the frozen profile"
  jq -e '
    .requests.cpu == "500m" and .requests.memory == "1Gi"
    and .limits.cpu == "2" and .limits.memory == "2Gi"
  ' <<<"${pg_resources}" >/dev/null \
    || fail "production-10k PostgreSQL resources do not match the frozen profile"
fi

percentile() {
  local name=$1
  local url=$2
  local timings="${OUTPUT_DIR}/.${PROFILE}-${LAYOUT}-${name}.timings"
  local failures=0
  : >"${timings}"
  for _ in $(seq 1 "${SAMPLES}"); do
    if ! curl -fsS -o /dev/null --max-time 10 -w '%{time_total}\n' \
      "${url}" >>"${timings}"; then
      printf '99\n' >>"${timings}"
      failures=$((failures + 1))
    fi
  done
  sort -n "${timings}" | awk -v failures="${failures}" '
    { values[NR] = $1 * 1000 }
    END {
      if (NR == 0) exit 1
      p50 = int(NR * 0.50); if (p50 < NR * 0.50) p50++; if (p50 < 1) p50 = 1
      p95 = int(NR * 0.95); if (p95 < NR * 0.95) p95++; if (p95 < 1) p95 = 1
      p99 = int(NR * 0.99); if (p99 < NR * 0.99) p99++; if (p99 < 1) p99 = 1
      printf "{\"p50_ms\":%.3f,\"p95_ms\":%.3f,\"p99_ms\":%.3f,\"failures\":%d}", values[p50], values[p95], values[p99], failures
    }'
  unlink "${timings}"
}

cluster_result=$(percentile cluster \
  "${base_url}/api/v1/graph?level=cluster")
namespace_result=$(percentile namespace \
  "${base_url}/api/v1/graph?level=namespace&namespace=${TARGET_NAMESPACE}")
blast_result=$(percentile blast \
  "${base_url}/api/v1/blast-radius/${TARGET_NAMESPACE}/ConfigMap/cm-00000")

cluster_p95=$(jq -r '.p95_ms' <<<"${cluster_result}")
namespace_p95=$(jq -r '.p95_ms' <<<"${namespace_result}")
blast_p95=$(jq -r '.p95_ms' <<<"${blast_result}")

for result_name in cluster_result namespace_result blast_result; do
  result_value=${!result_name}
  (( $(jq -r '.failures' <<<"${result_value}") == 0 )) \
    || fail "${result_name%_result} endpoint returned one or more failed samples"
done

within_limit() { awk -v actual="$1" -v limit="$2" 'BEGIN { exit !(actual <= limit) }'; }
within_limit "${cluster_p95}" "${CLUSTER_P95_LIMIT_MS}" \
  || fail "cluster-view p95 ${cluster_p95}ms exceeds ${CLUSTER_P95_LIMIT_MS}ms"
within_limit "${blast_p95}" "${BLAST_P95_LIMIT_MS}" \
  || fail "blast-radius p95 ${blast_p95}ms exceeds ${BLAST_P95_LIMIT_MS}ms"
namespace_gated=true
if [[ "${LAYOUT}" == "single-large-namespace" && "${PROFILE}" == "production-10k" ]]; then
  namespace_gated=false
else
  within_limit "${namespace_p95}" "${NAMESPACE_P95_LIMIT_MS}" \
    || fail "namespace-view p95 ${namespace_p95}ms exceeds ${NAMESPACE_P95_LIMIT_MS}ms"
fi

fixture_counts='{"configmaps":0,"deployments":0,"replicasets":0,"services":0,"total":0}'
IFS=',' read -r -a fixture_namespaces <<<"${NAMESPACES_CSV}"
for fixture_namespace in "${fixture_namespaces[@]}"; do
  [[ -n "${fixture_namespace}" ]] || fail "fixture namespace list contains an empty entry"
  namespace_counts=$(kubectl get configmaps,deployments.apps,replicasets.apps,services \
    -n "${fixture_namespace}" -o json | jq -c '
      reduce .items[] as $item (
        {configmaps: 0, deployments: 0, replicasets: 0, services: 0};
        if $item.kind == "ConfigMap" and ($item.metadata.name | test("^cm-[0-9]{5}$")) then .configmaps += 1
        elif $item.kind == "Deployment" and ($item.metadata.name | test("^dep-[0-9]{4}$")) then .deployments += 1
        elif $item.kind == "ReplicaSet" and (($item.metadata.ownerReferences // []) | any(.kind == "Deployment" and (.name | test("^dep-[0-9]{4}$")))) then .replicasets += 1
        elif $item.kind == "Service" and ($item.metadata.name | test("^svc-[0-9]{4}$")) then .services += 1
        else . end
      )')
  fixture_counts=$(jq -cn --argjson left "${fixture_counts}" --argjson right "${namespace_counts}" '
    {
      configmaps: ($left.configmaps + $right.configmaps),
      deployments: ($left.deployments + $right.deployments),
      replicasets: ($left.replicasets + $right.replicasets),
      services: ($left.services + $right.services)
    }
    | .total = (.configmaps + .deployments + .replicasets + .services)')
done

if [[ "${PROFILE}" == "default-5k" ]]; then
  expected_configmaps=5000
  expected_deployments=1000
  expected_services=200
else
  expected_configmaps=10000
  expected_deployments=2000
  expected_services=400
fi
jq -e \
  --argjson configmaps "${expected_configmaps}" \
  --argjson deployments "${expected_deployments}" \
  --argjson services "${expected_services}" '
    .configmaps == $configmaps
    and .deployments == $deployments
    and .services == $services
    and .replicasets >= $deployments
  ' <<<"${fixture_counts}" >/dev/null \
  || fail "fixture object counts do not match the selected ${PROFILE} profile"

app_pod_json=$(kubectl get pods -n "${NAMESPACE}" \
  -l "app.kubernetes.io/name=kubeatlas,app.kubernetes.io/instance=${RELEASE}" \
  -o json | jq -c '[.items[] | select(.metadata.labels["pod-template-hash"] != null)] | sort_by(.metadata.creationTimestamp) | last')
app_pod=$(jq -r '.metadata.name // empty' <<<"${app_pod_json}")
[[ -n "${app_pod}" ]] || fail "KubeAtlas Deployment Pod not found"
app_image_id=$(jq -r '.status.containerStatuses[] | select(.name == "kubeatlas") | .imageID' <<<"${app_pod_json}")
restart_count=$(jq -r '.status.containerStatuses[] | select(.name == "kubeatlas") | .restartCount' <<<"${app_pod_json}")
oom_killed=$(jq -r '[.status.containerStatuses[] | select(.name == "kubeatlas") | .lastState.terminated.reason == "OOMKilled"] | any' <<<"${app_pod_json}")
rss_kib=$(kubectl exec -n "${NAMESPACE}" "${app_pod}" -c kubeatlas -- \
  cat /proc/1/status | awk '$1 == "VmRSS:" { print $2; exit }')
[[ "${rss_kib}" =~ ^[0-9]+$ ]] || fail "could not read KubeAtlas RSS"
(( restart_count == 0 )) || fail "KubeAtlas restarted ${restart_count} times during the performance gate"
[[ "${oom_killed}" == "false" ]] || fail "KubeAtlas shows an OOMKilled termination"

pg_pod_json=$(kubectl get pods -n "${NAMESPACE}" -l "cnpg.io/cluster=${PG_CLUSTER}" \
  -o json | jq -c '[.items[] | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | sort_by(.metadata.creationTimestamp) | last')
pg_image_id=$(jq -r '.status.containerStatuses[] | select(.name == "postgres") | .imageID' <<<"${pg_pod_json}")
[[ "${app_image_id}" == *@sha256:* ]] || fail "application imageID is not immutable"
[[ "${pg_image_id}" == *@sha256:* ]] || fail "PostgreSQL imageID is not immutable"

server_version=$(kubectl version -o json | jq -r '.serverVersion.gitVersion')
docker_version=$(docker version --format '{{.Server.Version}}' 2>/dev/null || printf 'unavailable')
docker_desktop_version=$(docker version --format '{{.Server.Platform.Name}}' 2>/dev/null || printf 'unavailable')
cpu_model=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || uname -m)
host_memory_bytes=$(sysctl -n hw.memsize 2>/dev/null || printf '0')
chart_manifest_sha=$(helm get manifest "${RELEASE}" -n "${NAMESPACE}" | sha256_stream)

jq -n \
  --arg schema 'https://kubeatlas.lithastra.com/schemas/v160-performance-evidence-v1.json' \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg profile "${PROFILE}" --arg layout "${LAYOUT}" \
  --arg target_namespace "${TARGET_NAMESPACE}" --arg namespaces "${NAMESPACES_CSV}" \
  --arg git_sha "${head_sha}" --argjson dirty "${dirty}" \
  --arg context "$(kubectl config current-context)" --arg kubernetes "${server_version}" \
  --arg docker "${docker_version}" --arg docker_desktop "${docker_desktop_version}" \
  --arg os "$(uname -s)" --arg arch "$(uname -m)" --arg kernel "$(uname -r)" \
  --arg cpu "${cpu_model}" --argjson host_memory_bytes "${host_memory_bytes}" \
  --arg app_image_id "${app_image_id}" --arg pg_image_id "${pg_image_id}" \
  --arg chart_manifest_sha256 "${chart_manifest_sha}" \
  --argjson app_resources "${app_resources}" --argjson pg_resources "${pg_resources}" \
  --argjson fixture_counts "${fixture_counts}" --argjson samples "${SAMPLES}" \
  --argjson cluster "${cluster_result}" --argjson namespace_result "${namespace_result}" \
  --argjson blast "${blast_result}" --argjson namespace_gated "${namespace_gated}" \
  --argjson rss_bytes "$((rss_kib * 1024))" --argjson restart_count "${restart_count}" \
  --argjson oom_killed "${oom_killed}" '
  {
    "$schema": $schema,
    captured_at: $captured_at,
    candidate: {git_sha: $git_sha, dirty: $dirty, app_image_id: $app_image_id, postgres_image_id: $pg_image_id, chart_manifest_sha256: $chart_manifest_sha256},
    environment: {kubernetes_context: $context, kubernetes_server_version: $kubernetes, docker_server_version: $docker, docker_desktop: $docker_desktop, os: $os, arch: $arch, kernel: $kernel, cpu: $cpu, host_memory_bytes: $host_memory_bytes},
    profile: {name: $profile, layout: $layout, application_resources: $app_resources, postgres_resources: $pg_resources},
    fixture: {namespaces_csv: $namespaces, target_namespace: $target_namespace, counts: $fixture_counts, samples_per_endpoint: $samples},
    targets_ms: {cluster_view_p95: 1000, namespace_view_p95: 1000, blast_radius_p95: 500},
    results: {cluster_view: $cluster, namespace_view: ($namespace_result + {gated: $namespace_gated}), blast_radius: $blast},
    process: {rss_bytes: $rss_bytes, restart_count: $restart_count, oom_killed: $oom_killed},
    status: "pass"
  }' >"${output_file}"

pass "${PROFILE}/${LAYOUT} evidence written to ${output_file}"
printf '  cluster p95: %sms; namespace p95: %sms (gated=%s); blast p95: %sms\n' \
  "${cluster_p95}" "${namespace_p95}" "${namespace_gated}" "${blast_p95}"

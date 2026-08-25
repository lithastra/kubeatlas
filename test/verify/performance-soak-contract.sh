#!/usr/bin/env bash

# Fast CI contract: construct synthetic bounded evidence, prove both verifiers
# accept the complete shape, then prove a single failed gate is rejected.
# Timestamps model 168 hours; this test does not pretend to run a real soak.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "${ROOT_DIR}"

TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT
SHA=0123456789abcdef0123456789abcdef01234567
DIGEST=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
PG_DIGEST=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

write_performance() {
  local profile=$1 layout=$2 path=$3 configmaps=$4 deployments=$5 services=$6 namespaces=$7 gated=$8 namespace_p95=$9
  local app_resources pg_resources
  if [[ "${profile}" == "default-5k" ]]; then
    app_resources='{"requests":{"cpu":"100m","memory":"128Mi"},"limits":{"cpu":"500m","memory":"512Mi"}}'
    pg_resources='{}'
  else
    app_resources='{"requests":{"cpu":"500m","memory":"512Mi"},"limits":{"cpu":"2","memory":"2Gi"}}'
    pg_resources='{"requests":{"cpu":"500m","memory":"1Gi"},"limits":{"cpu":"2","memory":"2Gi"}}'
  fi
  namespace_csv=$(seq 0 $((namespaces - 1)) | awk '{printf "%sstress-test-%02d", sep, $1; sep=","}')
  jq -n \
    --arg profile "${profile}" --arg layout "${layout}" --arg sha "${SHA}" \
    --arg app_image "example.invalid/kubeatlas@sha256:${DIGEST}" \
    --arg pg_image "example.invalid/postgres@sha256:${PG_DIGEST}" \
    --arg namespaces "${namespace_csv}" --argjson app_resources "${app_resources}" \
    --argjson pg_resources "${pg_resources}" --argjson configmaps "${configmaps}" \
    --argjson deployments "${deployments}" --argjson services "${services}" \
    --argjson gated "${gated}" --argjson namespace_p95 "${namespace_p95}" '
    {
      "$schema":"https://kubeatlas.lithastra.com/schemas/v160-performance-evidence-v1.json",
      captured_at:"2026-08-24T00:00:00Z",status:"pass",
      candidate:{git_sha:$sha,dirty:false,app_image_id:$app_image,postgres_image_id:$pg_image,chart_manifest_sha256:"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
      environment:{kubernetes_context:"docker-desktop",kubernetes_server_version:"v1.36.1",docker_server_version:"29.0",docker_desktop:"Docker Desktop",os:"Darwin",arch:"arm64",kernel:"test",cpu:"test",host_memory_bytes:17179869184},
      profile:{name:$profile,layout:$layout,application_resources:$app_resources,postgres_resources:$pg_resources},
      fixture:{namespaces_csv:$namespaces,target_namespace:($namespaces|split(",")[0]),counts:{configmaps:$configmaps,deployments:$deployments,replicasets:$deployments,services:$services,total:($configmaps+$deployments+$deployments+$services)},samples_per_endpoint:100},
      targets_ms:{cluster_view_p95:1000,namespace_view_p95:1000,blast_radius_p95:500},
      results:{cluster_view:{p50_ms:100,p95_ms:500,p99_ms:600,failures:0},namespace_view:{p50_ms:100,p95_ms:$namespace_p95,p99_ms:$namespace_p95,failures:0,gated:$gated},blast_radius:{p50_ms:10,p95_ms:100,p99_ms:120,failures:0}},
      process:{rss_bytes:104857600,restart_count:0,oom_killed:false}
    }' >"${path}"
}

mkdir -p "${TMP}/performance"
write_performance default-5k single-large-namespace "${TMP}/performance/default.json" 5000 1000 200 1 true 700
write_performance production-10k distributed "${TMP}/performance/distributed.json" 10000 2000 400 10 true 800
write_performance production-10k single-large-namespace "${TMP}/performance/pathological.json" 10000 2000 400 1 false 1400
bash test/verify/v160-performance-evidence.sh \
  "${TMP}/performance/default.json" "${TMP}/performance/distributed.json" "${TMP}/performance/pathological.json"

# The random sentinel Secret is intentionally unreferenced, so no
# reference-only graph placeholder exists for its detail endpoint. Keep the
# runner's one narrow 404 allowance under the fast contract: other security
# surfaces must still require HTTP 200.
grep -Fq \
  '"/api/v1/resources/${NAMESPACE}/Secret/v160-soak-sentinel" true' \
  test/soak/v160-soak.sh
grep -Fq \
  '"${allow_not_found}" == "true" && "${http_code}" == "404"' \
  test/soak/v160-soak.sh

# The v1.5.2 upgrade verifier must not keep polling a dead kubectl tunnel.
# A request can race the application listener during a rollout, which makes
# kubectl port-forward exit even though the Pod becomes healthy moments later.
grep -Fq \
  'kubectl rollout status --namespace "${NS}" "deployment/${RELEASE}"' \
  test/verify/v152-secret-boundary.sh
grep -Fq \
  'if [[ -z "${PF_PID}" ]] || ! kill -0 "${PF_PID}" 2>/dev/null; then' \
  test/verify/v152-secret-boundary.sh
grep -Fq \
  'start_port_forward_process' \
  test/verify/v152-secret-boundary.sh

jq '.results.cluster_view.p95_ms = 1001' "${TMP}/performance/default.json" >"${TMP}/performance/invalid.json"
if bash test/verify/v160-performance-evidence.sh \
  "${TMP}/performance/invalid.json" "${TMP}/performance/distributed.json" "${TMP}/performance/pathological.json" \
  >/dev/null 2>&1; then
  echo "performance verifier accepted an over-target cluster p95" >&2
  exit 1
fi

mkdir -p "${TMP}/soak/logs"
START=2000000000
FINISH=$((START + 604800))
jq -cn --argjson start "${START}" '
  range(0; 2016) as $index
  | ($start + ($index * 300)) as $captured
  | {
      "$schema":"https://kubeatlas.lithastra.com/schemas/v160-soak-sample-v1.json",
      captured_at_epoch:$captured,
      phase:(if $index < 288 then "warmup" elif $index < 576 then "baseline" else "steady" end),
      load_class:"normal",
      process:{rss_bytes:104857600,goroutines:20,queue_depth:0,pod_uid:(if $index < 576 then "pod-before" else "pod-after" end),restart_count:0,oom_killed:false},
      health:{ready:true,kubernetes_api_reachable:true,storage_reachable:true,graph_synced:true},
      endpoints:{cluster:{failure:false,latency_ms:200},namespace:{failure:false,latency_ms:300},blast_radius:{failure:false,latency_ms:50}},
      counter_deltas:{snapshot_queue_drop:0,snapshot_write_failed:0,otel_dropped:0},
      sentinel_absent:true
    }
' >"${TMP}/soak/samples.jsonl"

jq -cn '
  [
    {name:"app-restart",recovery_seconds:30,status:"pass"},
    {name:"resource-storm",recovery_seconds:0,status:"pass"},
    {name:"snapshot-write-storm",recovery_seconds:0,status:"pass"},
    {name:"postgresql-interruption",recovery_seconds:60,status:"pass"},
    {name:"api-server-interruption",recovery_seconds:60,status:"pass"},
    {name:"otel-overload",recovery_seconds:0,status:"not-applicable"},
    {name:"final-upgrade-restore",recovery_seconds:0,status:"pass"}
  ][]
  | {"$schema":"https://kubeatlas.lithastra.com/schemas/v160-soak-event-v1.json",captured_at_epoch:(if .name == "final-upgrade-restore" then 2000604800 else 2000000000 end),name:.name,status:.status,recovery_seconds:.recovery_seconds,sentinel_absent:true,details:{}}
' >"${TMP}/soak/events.jsonl"

if command -v sha256sum >/dev/null 2>&1; then
  samples_hash=$(sha256sum "${TMP}/soak/samples.jsonl" | awk '{print $1}')
  events_hash=$(sha256sum "${TMP}/soak/events.jsonl" | awk '{print $1}')
else
  samples_hash=$(shasum -a 256 "${TMP}/soak/samples.jsonl" | awk '{print $1}')
  events_hash=$(shasum -a 256 "${TMP}/soak/events.jsonl" | awk '{print $1}')
fi
jq -n \
  --arg sha "${SHA}" --arg digest "${DIGEST}" --arg pg_digest "${PG_DIGEST}" \
  --arg samples_hash "${samples_hash}" --arg events_hash "${events_hash}" \
  --argjson start "${START}" --argjson finish "${FINISH}" '
  {
    "$schema":"https://kubeatlas.lithastra.com/schemas/v160-soak-evidence-v1.json",status:"pass",
    candidate:{git_sha:$sha,dirty:false,app_image_id:("example.invalid/kubeatlas@sha256:"+$digest),postgres_image_id:("example.invalid/postgres@sha256:"+$pg_digest)},
    environment:{kubernetes_context:"docker-desktop",kubernetes_server_version:"v1.36.1"},
    configuration:{duration_seconds:604800,warmup_seconds:86400,baseline_seconds:86400,sample_interval_seconds:300,otel_enabled:false},
    started_at_epoch:$start,finished_at_epoch:$finish,
    sentinel:{sha256:"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",raw_value_retained:false,scan_count:2016},
    expected_app_pod_uids:["pod-before","pod-after"],
    artifacts:[
      {path:"samples.jsonl",sha256:$samples_hash,sentinel_absent:true},
      {path:"events.jsonl",sha256:$events_hash,sentinel_absent:true}
    ]
  }' >"${TMP}/soak/manifest.json"

bash test/verify/v160-soak-evidence.sh "${TMP}/soak"
jq '.configuration.duration_seconds = 10' "${TMP}/soak/manifest.json" >"${TMP}/soak/invalid-manifest.json"
mv "${TMP}/soak/invalid-manifest.json" "${TMP}/soak/manifest.json"
if bash test/verify/v160-soak-evidence.sh "${TMP}/soak" >/dev/null 2>&1; then
  echo "soak verifier accepted a ten-second run" >&2
  exit 1
fi

printf 'performance/soak contract: complete synthetic evidence passes and weakened evidence fails\n'

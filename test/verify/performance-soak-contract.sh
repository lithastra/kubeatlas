#!/usr/bin/env bash

# Fast CI contract: construct synthetic bounded evidence, prove both verifiers
# accept the complete shape, then prove a single failed gate is rejected.
# Timestamps model 72 hours; this test does not pretend to run a real soak.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "${ROOT_DIR}"

TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT
SHA=0123456789abcdef0123456789abcdef01234567
DIGEST=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
PG_DIGEST=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

source test/soak/lib/v160-soak-event.sh
source test/soak/lib/v160-soak-http.sh

event_with_details=$(v160_soak_event_json app-restart pass 2 \
  '{"before_pod_uid":"pod-before","after_pod_uid":"pod-after"}')
jq -e '
  .name == "app-restart"
  and .status == "pass"
  and .recovery_seconds == 2
  and .sentinel_absent == true
  and .details == {before_pod_uid:"pod-before",after_pod_uid:"pod-after"}
' <<<"${event_with_details}" >/dev/null

event_without_details=$(v160_soak_event_json resource-storm pass 0)
jq -e '.name == "resource-storm" and .details == {}' \
  <<<"${event_without_details}" >/dev/null

if v160_soak_event_json app-restart pass 2 '{invalid-json}' >/dev/null 2>&1; then
  echo "soak event serializer accepted invalid details JSON" >&2
  exit 1
fi

# A single transport timeout must not invalidate three days of otherwise valid
# evidence, but a persistent timeout must still fail closed after the bounded
# attempt count. The helper returns the complete body/status framing unchanged.
mkdir -p "${TMP}/mock-bin"
cat >"${TMP}/mock-bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
count=0
[[ ! -f "${MOCK_CURL_COUNT_FILE}" ]] || count=$(<"${MOCK_CURL_COUNT_FILE}")
count=$((count + 1))
printf '%s\n' "${count}" >"${MOCK_CURL_COUNT_FILE}"
if (( count <= MOCK_CURL_FAILURES )); then
  printf 'curl: simulated timeout\n' >&2
  exit 28
fi
printf '{"safe":true}\n200\n'
EOF
chmod +x "${TMP}/mock-bin/curl"

retry_log="${TMP}/security-surface-retries.log"
retry_count="${TMP}/retry-count"
retry_response=$(PATH="${TMP}/mock-bin:${PATH}" \
  MOCK_CURL_COUNT_FILE="${retry_count}" MOCK_CURL_FAILURES=1 \
  v160_soak_http_get_with_retry 'http://127.0.0.1:18085/test' 30 3 0 \
    "${retry_log}" '/test')
[[ "${retry_response}" == $'{"safe":true}\n200' ]] \
  || { echo "security surface retry altered the response" >&2; exit 1; }
[[ "$(<"${retry_count}")" == "2" ]] \
  || { echo "security surface retry did not recover on the second attempt" >&2; exit 1; }
grep -Fq 'transport_failure endpoint=/test attempt=1/3 curl_exit=28' "${retry_log}"

persistent_count="${TMP}/persistent-count"
if PATH="${TMP}/mock-bin:${PATH}" \
  MOCK_CURL_COUNT_FILE="${persistent_count}" MOCK_CURL_FAILURES=3 \
  v160_soak_http_get_with_retry 'http://127.0.0.1:18085/test' 30 3 0 \
    "${retry_log}" '/test' >/dev/null; then
  echo "security surface retry accepted a persistent transport failure" >&2
  exit 1
fi
[[ "$(<"${persistent_count}")" == "3" ]] \
  || { echo "security surface retry exceeded its bounded attempt count" >&2; exit 1; }

write_performance() {
  local profile=$1 layout=$2 path=$3 configmaps=$4 deployments=$5 services=$6 namespaces=$7 gated=$8 namespace_p95=$9
  local app_resources pg_resources go_memory_limit_bytes
  if [[ "${profile}" == "default-5k" ]]; then
    app_resources='{"requests":{"cpu":"100m","memory":"128Mi"},"limits":{"cpu":"500m","memory":"512Mi"}}'
    pg_resources='{}'
    go_memory_limit_bytes=402653184
  else
    app_resources='{"requests":{"cpu":"500m","memory":"512Mi"},"limits":{"cpu":"2","memory":"2Gi"}}'
    pg_resources='{"requests":{"cpu":"500m","memory":"1Gi"},"limits":{"cpu":"2","memory":"2Gi"}}'
    go_memory_limit_bytes=1610612736
  fi
  namespace_csv=$(seq 0 $((namespaces - 1)) | awk '{printf "%sstress-test-%02d", sep, $1; sep=","}')
  jq -n \
    --arg profile "${profile}" --arg layout "${layout}" --arg sha "${SHA}" \
    --arg app_image "example.invalid/kubeatlas@sha256:${DIGEST}" \
    --arg pg_image "example.invalid/postgres@sha256:${PG_DIGEST}" \
    --arg namespaces "${namespace_csv}" --argjson go_memory_limit_bytes "${go_memory_limit_bytes}" --argjson app_resources "${app_resources}" \
    --argjson pg_resources "${pg_resources}" --argjson configmaps "${configmaps}" \
    --argjson deployments "${deployments}" --argjson services "${services}" \
    --argjson gated "${gated}" --argjson namespace_p95 "${namespace_p95}" '
    {
      "$schema":"https://kubeatlas.lithastra.com/schemas/v160-performance-evidence-v1.json",
      captured_at:"2026-08-24T00:00:00Z",status:"pass",
      candidate:{git_sha:$sha,dirty:false,app_image_id:$app_image,postgres_image_id:$pg_image,chart_manifest_sha256:"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
      environment:{kubernetes_context:"docker-desktop",kubernetes_server_version:"v1.36.1",docker_server_version:"29.0",docker_desktop:"Docker Desktop",os:"Darwin",arch:"arm64",kernel:"test",cpu:"test",host_memory_bytes:17179869184},
      profile:{name:$profile,layout:$layout,go_memory_limit_percent:75,go_memory_limit_bytes:$go_memory_limit_bytes,application_resources:$app_resources,postgres_resources:$pg_resources},
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

jq '.profile.go_memory_limit_bytes = 536870912' "${TMP}/performance/default.json" >"${TMP}/performance/wrong-memory-limit.json"
if bash test/verify/v160-performance-evidence.sh \
  "${TMP}/performance/wrong-memory-limit.json" "${TMP}/performance/distributed.json" "${TMP}/performance/pathological.json" \
  >/dev/null 2>&1; then
  echo "performance verifier accepted the wrong Go memory limit" >&2
  exit 1
fi

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
grep -Fq \
  'v160_soak_http_get_with_retry' \
  test/soak/v160-soak.sh
grep -Fq \
  'remained unreachable after ${SECURITY_SURFACE_ATTEMPTS} attempts' \
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

# The 72-hour runner must not attach its replacement tunnel to a terminating
# Pod. Require an explicitly different Ready Pod, a Pod-specific tunnel, and
# retry behavior when kubectl exits before the application becomes reachable.
grep -Fq \
  'select(.metadata.uid != $old_uid)' \
  test/soak/v160-soak.sh
grep -Fq \
  'kubectl delete pod -n "${NAMESPACE}" "${before_name}"' \
  test/soak/v160-soak.sh
grep -Fq \
  'start_port_forward "pod/${after_name}" "${remaining}"' \
  test/soak/v160-soak.sh
grep -Fq \
  'if [[ -z "${PF_PID}" ]] || ! kill -0 "${PF_PID}" 2>/dev/null; then' \
  test/soak/v160-soak.sh

jq '.results.cluster_view.p95_ms = 1001' "${TMP}/performance/default.json" >"${TMP}/performance/invalid.json"
if bash test/verify/v160-performance-evidence.sh \
  "${TMP}/performance/invalid.json" "${TMP}/performance/distributed.json" "${TMP}/performance/pathological.json" \
  >/dev/null 2>&1; then
  echo "performance verifier accepted an over-target cluster p95" >&2
  exit 1
fi

mkdir -p "${TMP}/soak/logs"
START=2000000000
FINISH=$((START + 259200))
jq -cn --argjson start "${START}" '
  range(0; 864) as $index
  | ($start + ($index * 300)) as $captured
  | {
      "$schema":"https://kubeatlas.lithastra.com/schemas/v160-soak-sample-v1.json",
      captured_at_epoch:$captured,
      phase:(if $index < 72 then "warmup" elif $index < 216 then "baseline" else "steady" end),
      load_class:"normal",
      process:{rss_bytes:104857600,goroutines:20,queue_depth:0,pod_uid:(if $index < 216 then "pod-before" else "pod-after" end),restart_count:0,oom_killed:false},
      health:{ready:true,kubernetes_api_reachable:true,storage_reachable:true,graph_synced:true},
      endpoints:{cluster:{failure:false,latency_ms:200},namespace:{failure:false,latency_ms:300},blast_radius:{failure:false,latency_ms:50}},
      counter_deltas:{snapshot_queue_drop:0,snapshot_write_failed:0,otel_dropped:0},
      sentinel_absent:true
    }
' >"${TMP}/soak/samples.jsonl"

jq -cn --argjson start "${START}" --argjson finish "${FINISH}" '
  [
    {name:"app-restart",recovery_seconds:30,status:"pass"},
    {name:"resource-storm",recovery_seconds:0,status:"pass"},
    {name:"snapshot-write-storm",recovery_seconds:0,status:"pass"},
    {name:"postgresql-interruption",recovery_seconds:60,status:"pass"},
    {name:"api-server-interruption",recovery_seconds:60,status:"pass"},
    {name:"otel-overload",recovery_seconds:0,status:"not-applicable"},
    {name:"final-upgrade-restore",recovery_seconds:0,status:"pass"}
  ][]
  | ({"app-restart":64800,"resource-storm":86400,"snapshot-write-storm":108000,"postgresql-interruption":129600,"api-server-interruption":151200,"otel-overload":172800}[.name]) as $offset
  | {"$schema":"https://kubeatlas.lithastra.com/schemas/v160-soak-event-v1.json",captured_at_epoch:(if .name == "final-upgrade-restore" then $finish else $start + $offset + 120 end),name:.name,status:.status,recovery_seconds:.recovery_seconds,sentinel_absent:true,details:(if .name == "postgresql-interruption" then {injection:"cnpg-hibernation",outage_observed:true,original_primary_uid:"pg-before",replacement_primary_uid:"pg-after",replacement_ready_seconds:10,recovery_seconds:60,outage_observed_at_epoch:($start + $offset)} else {} end)}
' >"${TMP}/soak/events.jsonl"

if command -v sha256sum >/dev/null 2>&1; then
  samples_hash=$(sha256sum "${TMP}/soak/samples.jsonl" | awk '{print $1}')
  events_hash=$(sha256sum "${TMP}/soak/events.jsonl" | awk '{print $1}')
else
  samples_hash=$(shasum -a 256 "${TMP}/soak/samples.jsonl" | awk '{print $1}')
  if command -v sha256sum >/dev/null 2>&1; then
    events_hash=$(sha256sum "${TMP}/soak/events.jsonl" | awk '{print $1}')
  else
    events_hash=$(shasum -a 256 "${TMP}/soak/events.jsonl" | awk '{print $1}')
  fi
fi
jq -n \
  --arg sha "${SHA}" --arg digest "${DIGEST}" --arg pg_digest "${PG_DIGEST}" \
  --arg samples_hash "${samples_hash}" --arg events_hash "${events_hash}" \
  --argjson start "${START}" --argjson finish "${FINISH}" '
  {
    "$schema":"https://kubeatlas.lithastra.com/schemas/v160-soak-evidence-v2.json",status:"pass",
    candidate:{git_sha:$sha,dirty:false,app_image_id:("example.invalid/kubeatlas@sha256:"+$digest),postgres_image_id:("example.invalid/postgres@sha256:"+$pg_digest)},
    environment:{kubernetes_context:"docker-desktop",kubernetes_server_version:"v1.36.1"},
    configuration:{
      duration_seconds:259200,warmup_seconds:21600,baseline_seconds:43200,growth_window_seconds:43200,
      sample_interval_seconds:300,otel_enabled:false,
      security_surface_timeout_seconds:30,security_surface_attempts:3,
      security_surface_retry_delay_seconds:2
    },
    started_at_epoch:$start,finished_at_epoch:$finish,
    sentinel:{sha256:"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",raw_value_retained:false,scan_count:864},
    expected_app_pod_uids:["pod-before","pod-after"],
    artifacts:[
      {path:"samples.jsonl",sha256:$samples_hash,sentinel_absent:true},
      {path:"events.jsonl",sha256:$events_hash,sentinel_absent:true}
    ]
  }' >"${TMP}/soak/manifest.json"

bash test/verify/v160-soak-evidence.sh "${TMP}/soak"
# Refresh the artifact hash so these failures test event semantics, not hashing.
cp "${TMP}/soak/events.jsonl" "${TMP}/valid-events.jsonl"
cp "${TMP}/soak/manifest.json" "${TMP}/valid-manifest.json"
cp "${TMP}/soak/samples.jsonl" "${TMP}/valid-samples.jsonl"

# Pin the runner independently of the synthetic verifier fixtures. In
# particular, shortening the duration alone must not leave events after it.
for contract in \
  'DURATION_SECONDS="${KUBEATLAS_SOAK_DURATION_SECONDS:-259200}"' \
  'WARMUP_SECONDS=21600' 'BASELINE_SECONDS=43200' 'GROWTH_WINDOW_SECONDS=43200' \
  'DURATION_SECONDS >= 259200' 'KUBEATLAS_CONFIRM_72H_SOAK' \
  'app_restart_done == 0 && elapsed >= 64800' \
  'resource_storm_done == 0 && elapsed >= 86400' \
  'snapshot_storm_done == 0 && elapsed >= 108000' \
  'pg_interruption_done == 0 && elapsed >= 129600' \
  'api_interruption_done == 0 && elapsed >= 151200' \
  'otel_overload_done == 0 && elapsed >= 172800'; do
  grep -Fq "${contract}" test/soak/v160-soak.sh
done

expect_soak_rejected() {
  if bash test/verify/v160-soak-evidence.sh "${TMP}/soak" >/dev/null 2>&1; then
    echo "soak verifier accepted weakened evidence: $1" >&2
    exit 1
  fi
}

for mutation in \
  '.configuration.duration_seconds = 259199' \
  '.finished_at_epoch = .started_at_epoch + 259199' \
  '.configuration.warmup_seconds = 86400' \
  '.configuration.baseline_seconds = 86400' \
  '.configuration.growth_window_seconds = 86400' \
  '.["$schema"] = "https://kubeatlas.lithastra.com/schemas/v160-soak-evidence-v1.json"'; do
  jq "${mutation}" "${TMP}/valid-manifest.json" >"${TMP}/soak/manifest.json"
  expect_soak_rejected "${mutation}"
done

for mutation in \
  'select(.name != "postgresql-interruption")' \
  'select(.name != "final-upgrade-restore")' \
  'if .name == "api-server-interruption" then .captured_at_epoch = 2000000000 else . end' \
  'if .name == "otel-overload" then .captured_at_epoch = 2000259200 else . end' \
  'if .name == "final-upgrade-restore" then .captured_at_epoch = 2000259199 else . end'; do
  jq -c "${mutation}" "${TMP}/valid-events.jsonl" >"${TMP}/soak/events.jsonl"
  events_hash=$(shasum -a 256 "${TMP}/soak/events.jsonl" | awk '{print $1}')
  jq --arg hash "${events_hash}" '(.artifacts[] | select(.path == "events.jsonl").sha256) = $hash' \
    "${TMP}/valid-manifest.json" >"${TMP}/soak/manifest.json"
  expect_soak_rejected "${mutation}"
done
cp "${TMP}/valid-events.jsonl" "${TMP}/soak/events.jsonl"

# Rehash every mutation so rejection proves semantics, not checksum failure.
# Include the last partial 12-hour window (hours 66-72), empty normal-load
# windows, continuity, loss, sentinel, OOM and unexpected restarts.
for mutation in \
  'if .captured_at_epoch >= 2000237600 then .process.rss_bytes *= 1.21 else . end' \
  'if .captured_at_epoch >= 2000064800 and .captured_at_epoch < 2000108000 then .process.goroutines = 25 else . end' \
  'if .captured_at_epoch >= 2000237600 then .process.queue_depth = 1 else . end' \
  'if .captured_at_epoch >= 2000237600 then .load_class = "intentional-overload" else . end' \
  'select(.captured_at_epoch < 2000100000 or .captured_at_epoch > 2000100900)' \
  '.counter_deltas.snapshot_queue_drop = 1' \
  '.sentinel_absent = false' '.process.oom_killed = true' '.process.restart_count = 1'; do
  jq -c "${mutation}" "${TMP}/valid-samples.jsonl" >"${TMP}/soak/samples.jsonl"
  samples_hash=$(shasum -a 256 "${TMP}/soak/samples.jsonl" | awk '{print $1}')
  jq --arg hash "${samples_hash}" '(.artifacts[] | select(.path == "samples.jsonl").sha256) = $hash' \
    "${TMP}/valid-manifest.json" >"${TMP}/soak/manifest.json"
  expect_soak_rejected "${mutation}"
done

# Exactly 20% RSS growth is permitted; the 21% case above must fail.
jq -c 'if .captured_at_epoch >= 2000064800 then .process.rss_bytes *= 1.2 else . end' \
  "${TMP}/valid-samples.jsonl" >"${TMP}/soak/samples.jsonl"
samples_hash=$(shasum -a 256 "${TMP}/soak/samples.jsonl" | awk '{print $1}')
jq --arg hash "${samples_hash}" '(.artifacts[] | select(.path == "samples.jsonl").sha256) = $hash' \
  "${TMP}/valid-manifest.json" >"${TMP}/soak/manifest.json"
bash test/verify/v160-soak-evidence.sh "${TMP}/soak"
cp "${TMP}/valid-samples.jsonl" "${TMP}/soak/samples.jsonl"
cp "${TMP}/valid-manifest.json" "${TMP}/soak/manifest.json"

for mutation in \
  '.details.outage_observed = false' \
  '.details.replacement_primary_uid = .details.original_primary_uid' \
  'del(.details.original_primary_uid)' \
  '.details.replacement_ready_seconds = 121' \
  '.details.recovery_seconds = -1' \
  '.details.outage_observed_at_epoch = 0'; do
  jq -c "if .name == \"postgresql-interruption\" then ${mutation} else . end" \
    "${TMP}/valid-events.jsonl" >"${TMP}/soak/events.jsonl"
  events_hash=$(shasum -a 256 "${TMP}/soak/events.jsonl" | awk '{print $1}')
  jq --arg hash "${events_hash}" '(.artifacts[] | select(.path == "events.jsonl").sha256) = $hash' \
    "${TMP}/valid-manifest.json" >"${TMP}/soak/manifest.json"
  if bash test/verify/v160-soak-evidence.sh "${TMP}/soak" >/dev/null 2>&1; then
    echo "soak verifier accepted invalid PostgreSQL lifecycle evidence: ${mutation}" >&2
    exit 1
  fi
done
cp "${TMP}/valid-events.jsonl" "${TMP}/soak/events.jsonl"
cp "${TMP}/valid-manifest.json" "${TMP}/soak/manifest.json"
jq '.configuration.security_surface_attempts = 4' "${TMP}/soak/manifest.json" >"${TMP}/soak/invalid-manifest.json"
mv "${TMP}/soak/invalid-manifest.json" "${TMP}/soak/manifest.json"
if bash test/verify/v160-soak-evidence.sh "${TMP}/soak" >/dev/null 2>&1; then
  echo "soak verifier accepted a weakened security-surface retry contract" >&2
  exit 1
fi
jq '.configuration.security_surface_attempts = 3' "${TMP}/soak/manifest.json" >"${TMP}/soak/valid-manifest.json"
mv "${TMP}/soak/valid-manifest.json" "${TMP}/soak/manifest.json"
jq '.configuration.duration_seconds = 10' "${TMP}/soak/manifest.json" >"${TMP}/soak/invalid-manifest.json"
mv "${TMP}/soak/invalid-manifest.json" "${TMP}/soak/manifest.json"
if bash test/verify/v160-soak-evidence.sh "${TMP}/soak" >/dev/null 2>&1; then
  echo "soak verifier accepted a ten-second run" >&2
  exit 1
fi

printf 'performance/soak contract: complete synthetic evidence passes and weakened evidence fails\n'

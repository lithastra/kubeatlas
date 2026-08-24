#!/usr/bin/env bash

# Fail-closed verifier for one completed v1.6 168-hour soak directory.

set -euo pipefail

fail() { printf 'v1.6 soak evidence: %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS: %s\n' "$*"; }

(( $# == 1 )) || fail "usage: $0 EVIDENCE_DIRECTORY"
EVIDENCE_DIR=$1
MANIFEST="${EVIDENCE_DIR}/manifest.json"
SAMPLES="${EVIDENCE_DIR}/samples.jsonl"
EVENTS="${EVIDENCE_DIR}/events.jsonl"

for command_name in jq grep awk; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "missing required command: ${command_name}"
done
if command -v sha256sum >/dev/null 2>&1; then
  sha256_file() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256_file() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  fail "missing required SHA-256 command: sha256sum or shasum"
fi

[[ -d "${EVIDENCE_DIR}" ]] || fail "directory not found: ${EVIDENCE_DIR}"
for required_file in "${MANIFEST}" "${SAMPLES}" "${EVENTS}"; do
  [[ -s "${required_file}" ]] || fail "required evidence file is absent or empty: ${required_file}"
done
jq -e . "${MANIFEST}" >/dev/null || fail "manifest.json is not valid JSON"
jq -e . "${SAMPLES}" >/dev/null || fail "samples.jsonl contains invalid JSON"
jq -e . "${EVENTS}" >/dev/null || fail "events.jsonl contains invalid JSON"

jq -e '
  def git_sha: type == "string" and test("^[0-9a-f]{40}$");
  def sha256: type == "string" and test("^[0-9a-f]{64}$");
  def image_id: type == "string" and test("@sha256:[0-9a-f]{64}$");
  .["$schema"] == "https://kubeatlas.lithastra.com/schemas/v160-soak-evidence-v1.json"
  and .status == "pass"
  and (.candidate.git_sha | git_sha)
  and .candidate.dirty == false
  and (.candidate.app_image_id | image_id)
  and (.candidate.postgres_image_id | image_id)
  and .environment.kubernetes_context == "docker-desktop"
  and (.environment.kubernetes_server_version | test("^v1\\.(34|35|36)\\."))
  and .configuration.duration_seconds >= 604800
  and .configuration.warmup_seconds == 86400
  and .configuration.baseline_seconds == 86400
  and (.configuration.sample_interval_seconds >= 60 and .configuration.sample_interval_seconds <= 300)
  and (.finished_at_epoch - .started_at_epoch >= .configuration.duration_seconds)
  and (.sentinel.sha256 | sha256)
  and .sentinel.raw_value_retained == false
  and (.sentinel.scan_count | type == "number" and . > 0)
  and (.expected_app_pod_uids | type == "array" and length == 2 and (unique | length) == 2)
  and (.artifacts | type == "array" and length >= 2)
  and all(.artifacts[]; .sentinel_absent == true and (.sha256 | sha256))
' "${MANIFEST}" >/dev/null || fail "manifest does not satisfy the frozen v1.6 soak contract"

started_at=$(jq -r '.started_at_epoch' "${MANIFEST}")
finished_at=$(jq -r '.finished_at_epoch' "${MANIFEST}")
duration=$(jq -r '.configuration.duration_seconds' "${MANIFEST}")
warmup=$(jq -r '.configuration.warmup_seconds' "${MANIFEST}")
baseline=$(jq -r '.configuration.baseline_seconds' "${MANIFEST}")
interval=$(jq -r '.configuration.sample_interval_seconds' "${MANIFEST}")
otel_enabled=$(jq -r '.configuration.otel_enabled' "${MANIFEST}")
expected_uids=$(jq -c '.expected_app_pod_uids' "${MANIFEST}")

jq -se \
  --argjson start "${started_at}" \
  --argjson finish "${finished_at}" \
  --argjson duration "${duration}" \
  --argjson warmup "${warmup}" \
  --argjson baseline "${baseline}" \
  --argjson interval "${interval}" \
  --argjson expected_uids "${expected_uids}" '
  def p95:
    sort as $values
    | ($values | length) as $n
    | if $n == 0 then null else $values[((($n * 95 + 99) / 100 | floor) - 1)] end;
  def metric_p95($rows; $field): [$rows[] | getpath($field)] | p95;
  def healthy_normal:
    .load_class == "normal"
    and .health.ready == true
    and .health.kubernetes_api_reachable == true
    and .health.storage_reachable == true
    and .health.graph_synced == true
    and .endpoints.cluster.failure == false
    and .endpoints.namespace.failure == false
    and .endpoints.blast_radius.failure == false
    and .counter_deltas.snapshot_queue_drop == 0
    and .counter_deltas.snapshot_write_failed == 0
    and .counter_deltas.otel_dropped == 0;

  sort_by(.captured_at_epoch) as $rows
  | ($start + $warmup) as $baseline_start
  | ($baseline_start + $baseline) as $baseline_end
  | [$rows[] | select(.captured_at_epoch >= $baseline_start and .captured_at_epoch < $baseline_end and .load_class == "normal")] as $baseline_rows
  | (metric_p95($baseline_rows; ["process", "rss_bytes"])) as $rss_baseline
  | (metric_p95($baseline_rows; ["process", "goroutines"])) as $goroutine_baseline
  | (metric_p95($baseline_rows; ["process", "queue_depth"])) as $queue_baseline
  | [$rows[] | select(.captured_at_epoch >= $baseline_end and .load_class == "normal")
      | . + {day: (((.captured_at_epoch - $baseline_end) / 86400) | floor)}]
    | group_by(.day) as $later_days
  | ($rows | length) >= (($duration / (2 * $interval)) | floor)
  and ($rows[0].captured_at_epoch <= $start + $interval)
  and ($rows[-1].captured_at_epoch >= $finish - (2 * $interval))
  and all(range(1; $rows | length);
    (($rows[.].captured_at_epoch - $rows[. - 1].captured_at_epoch) > 0)
    and (($rows[.].captured_at_epoch - $rows[. - 1].captured_at_epoch) <= (2 * $interval)))
  and all($rows[];
    .["$schema"] == "https://kubeatlas.lithastra.com/schemas/v160-soak-sample-v1.json"
    and (.phase == "warmup" or .phase == "baseline" or .phase == "steady" or .phase == "event")
    and (.load_class == "normal" or .load_class == "intentional-overload")
    and (.process.rss_bytes | type == "number" and . > 0)
    and (.process.goroutines | type == "number" and . > 0)
    and (.process.queue_depth | type == "number" and . >= 0)
    and .process.restart_count == 0
    and .process.oom_killed == false
    and (.process.pod_uid as $uid | $expected_uids | index($uid) != null)
    and .sentinel_absent == true
    and (if .load_class == "normal" then healthy_normal else true end))
  and ($baseline_rows | length) >= (($baseline / (2 * $interval)) | floor)
  and ($rss_baseline != null and $goroutine_baseline != null and $queue_baseline != null)
  and all($later_days[];
    (metric_p95(.; ["process", "rss_bytes"]) <= ($rss_baseline * 1.2))
    and (metric_p95(.; ["process", "goroutines"]) <= ($goroutine_baseline * 1.2))
    and (metric_p95(.; ["process", "queue_depth"]) <= (if $queue_baseline == 0 then 0 else ($queue_baseline * 1.2) end)))
' "${SAMPLES}" >/dev/null || fail "samples fail continuity, health, restart, loss, or sustained-growth checks"

jq -se --argjson otel_enabled "${otel_enabled}" --argjson start "${started_at}" --argjson finish "${finished_at}" '
  def passed($name): any(.[]; .name == $name and .status == "pass" and .sentinel_absent == true);
  def recovery_passed($name): any(.[]; .name == $name and .status == "pass" and .sentinel_absent == true and .recovery_seconds <= 120);
  all(.[];
    .["$schema"] == "https://kubeatlas.lithastra.com/schemas/v160-soak-event-v1.json"
    and (.status == "pass" or (.name == "otel-overload" and .status == "not-applicable"))
    and .sentinel_absent == true)
  and recovery_passed("app-restart")
  and passed("resource-storm")
  and passed("snapshot-write-storm")
  and recovery_passed("postgresql-interruption")
  and recovery_passed("api-server-interruption")
  and passed("final-upgrade-restore")
  and (length == 7)
  and ([.[].name] | unique | length == 7)
  and all(.[] | select(.name != "final-upgrade-restore"); .captured_at_epoch >= $start and .captured_at_epoch <= $finish)
  and any(.[]; .name == "final-upgrade-restore" and .captured_at_epoch >= $finish)
  and (if $otel_enabled then passed("otel-overload") else any(.[]; .name == "otel-overload" and .status == "not-applicable") end)
' "${EVENTS}" >/dev/null || fail "required failure, overload, and final recovery events are incomplete"

while IFS=$'\t' read -r relative_path expected_hash; do
  [[ "${relative_path}" =~ ^[A-Za-z0-9._/-]+$ ]] || fail "unsafe artifact path: ${relative_path}"
  [[ "${relative_path}" != /* && "${relative_path}" != ".." && "${relative_path}" != ../* && "${relative_path}" != */../* && "${relative_path}" != */.. ]] \
    || fail "artifact path escapes evidence directory: ${relative_path}"
  artifact_path="${EVIDENCE_DIR}/${relative_path}"
  [[ -f "${artifact_path}" ]] || fail "declared artifact not found: ${relative_path}"
  actual_hash=$(sha256_file "${artifact_path}")
  [[ "${actual_hash}" == "${expected_hash}" ]] || fail "artifact hash mismatch: ${relative_path}"
done < <(jq -r '.artifacts[] | [.path, .sha256] | @tsv' "${MANIFEST}")

candidate_sha=$(jq -r '.candidate.git_sha' "${MANIFEST}")
pass "168-hour soak evidence for candidate ${candidate_sha}"

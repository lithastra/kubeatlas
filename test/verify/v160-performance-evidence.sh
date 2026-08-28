#!/usr/bin/env bash

# Validate the complete v1.6 performance evidence set. This verifier consumes
# only bounded JSON summaries: it never needs API bodies or cluster dumps.

set -euo pipefail

fail() { printf 'v1.6 performance evidence: %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS: %s\n' "$*"; }

command -v jq >/dev/null 2>&1 || fail "missing required command: jq"
(( $# == 3 )) || fail "provide exactly three evidence files: default 5K, distributed 10K, and pathological 10K"

for evidence_file in "$@"; do
  [[ -f "${evidence_file}" ]] || fail "evidence file not found: ${evidence_file}"
  jq -e . "${evidence_file}" >/dev/null || fail "invalid JSON: ${evidence_file}"
done

jq -se '
  def sha256: type == "string" and test("^[0-9a-f]{64}$");
  def git_sha: type == "string" and test("^[0-9a-f]{40}$");
  def image_id: type == "string" and test("@sha256:[0-9a-f]{64}$");
  def base:
    .["$schema"] == "https://kubeatlas.lithastra.com/schemas/v160-performance-evidence-v1.json"
    and .status == "pass"
    and (.captured_at | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
    and (.candidate.git_sha | git_sha)
    and .candidate.dirty == false
    and (.candidate.app_image_id | image_id)
    and (.candidate.postgres_image_id | image_id)
    and (.candidate.chart_manifest_sha256 | sha256)
    and .environment.kubernetes_context == "docker-desktop"
    and (.environment.kubernetes_server_version | test("^v1\\.(34|35|36)\\."))
    and .environment.docker_server_version != "unavailable"
    and .environment.docker_desktop != "unavailable"
    and (.environment.host_memory_bytes | type == "number" and . > 0)
    and (.fixture.samples_per_endpoint | type == "number" and . >= 100)
    and (.fixture.counts.replicasets >= .fixture.counts.deployments)
    and (.fixture.counts.total == (
      .fixture.counts.configmaps + .fixture.counts.deployments
      + .fixture.counts.replicasets + .fixture.counts.services))
    and (.fixture.target_namespace as $target | ((.fixture.namespaces_csv | split(",")) | index($target) != null))
    and (.process.rss_bytes | type == "number" and . > 0)
    and .process.restart_count == 0
    and .process.oom_killed == false
    and .targets_ms == {cluster_view_p95: 1000, namespace_view_p95: 1000, blast_radius_p95: 500}
    and .results.cluster_view.failures == 0
    and .results.namespace_view.failures == 0
    and .results.blast_radius.failures == 0
    and (.results.cluster_view.p95_ms <= .targets_ms.cluster_view_p95)
    and (.results.blast_radius.p95_ms <= .targets_ms.blast_radius_p95);
  def default_resources:
    .profile.go_memory_limit_percent == 75
    and .profile.go_memory_limit_bytes == 402653184
    and .profile.application_resources == {
      requests: {cpu: "100m", memory: "128Mi"},
      limits: {cpu: "500m", memory: "512Mi"}
    };
  def production_resources:
    .profile.go_memory_limit_percent == 75
    and .profile.go_memory_limit_bytes == 1610612736
    and .profile.application_resources == {
      requests: {cpu: "500m", memory: "512Mi"},
      limits: {cpu: "2", memory: "2Gi"}
    }
    and .profile.postgres_resources == {
      requests: {cpu: "500m", memory: "1Gi"},
      limits: {cpu: "2", memory: "2Gi"}
    };
  def fixture($cm; $dep; $svc):
    .fixture.counts.configmaps == $cm
    and .fixture.counts.deployments == $dep
    and .fixture.counts.services == $svc;
  def default_5k:
    .profile.name == "default-5k"
    and .profile.layout == "single-large-namespace"
    and default_resources
    and fixture(5000; 1000; 200)
    and ((.fixture.namespaces_csv | split(",") | length) == 1)
    and .results.namespace_view.gated == true
    and (.results.namespace_view.p95_ms <= .targets_ms.namespace_view_p95);
  def production_distributed:
    .profile.name == "production-10k"
    and .profile.layout == "distributed"
    and production_resources
    and fixture(10000; 2000; 400)
    and ((.fixture.namespaces_csv | split(",") | length) == 10)
    and .results.namespace_view.gated == true
    and (.results.namespace_view.p95_ms <= .targets_ms.namespace_view_p95);
  def production_pathological:
    .profile.name == "production-10k"
    and .profile.layout == "single-large-namespace"
    and production_resources
    and fixture(10000; 2000; 400)
    and ((.fixture.namespaces_csv | split(",") | length) == 1)
    and .results.namespace_view.gated == false
    and (.results.namespace_view.p95_ms | type == "number" and . >= 0);

  length == 3
  and all(.[]; base)
  and ([.[].candidate.git_sha] | unique | length == 1)
  and ([.[].candidate.app_image_id] | unique | length == 1)
  and ([.[].candidate.postgres_image_id] | unique | length == 1)
  and ([.[] | select(.profile.name == "production-10k") | .candidate.chart_manifest_sha256] | unique | length == 1)
  and ([.[] | select(default_5k)] | length == 1)
  and ([.[] | select(production_distributed)] | length == 1)
  and ([.[] | select(production_pathological)] | length == 1)
' "$@" >/dev/null || fail "evidence set does not satisfy the frozen v1.6 performance contract"

candidate_sha=$(jq -r '.candidate.git_sha' "$1")
pass "complete performance evidence set for candidate ${candidate_sha}"

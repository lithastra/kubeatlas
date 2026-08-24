#!/usr/bin/env bash

# Guard the v1.6 operator-evidence contract against silent drift. Runtime
# behavior is covered by Go, Helm, kind, and chaos tests; this script verifies
# that the independently maintained wiring and documentation remain composed.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "${ROOT_DIR}"

fail() {
  printf 'operability contract: %s\n' "$*" >&2
  exit 1
}

require_text() {
  local file=$1
  local expected=$2
  grep -Fq -- "${expected}" "${file}" || fail "${file} is missing: ${expected}"
}

metrics_file=pkg/api/metrics.go
for metric in \
  kubeatlas_graph_observation_state \
  kubeatlas_kubernetes_api_reachable \
  kubeatlas_kubernetes_api_last_success_timestamp_seconds \
  kubeatlas_storage_reachable \
  kubeatlas_storage_durable \
  kubeatlas_storage_last_success_timestamp_seconds \
  kubeatlas_backup_status_available \
  kubeatlas_backup_last_success_timestamp_seconds \
  kubeatlas_backup_age_seconds \
  kubeatlas_snapshot_queue_drop_total \
  kubeatlas_snapshot_write_failed_total \
  kubeatlas_otel_dropped_total; do
  require_text "${metrics_file}" "${metric}"
done

for state in ObservationInitializing ObservationSynced ObservationDegraded ObservationStale; do
  require_text "${metrics_file}" "operations.${state}"
done

require_text cmd/kubeatlas/main.go 'kubernetesProbe := operations.Probe(client.Probe)'
require_text cmd/kubeatlas/main.go 'kubernetesProbe = mcMgr.Probe'
require_text pkg/multicluster/manager.go 'func (m *Manager) Probe(ctx context.Context) error'
require_text pkg/discovery/discovery.go 'Resource(namespaceGVR).List(ctx, metav1.ListOptions{Limit: 1})'
require_text pkg/store/postgres/store.go 's.pool.Ping(ctx)'

deployment=helm/kubeatlas/templates/deployment.yaml
for env_name in \
  KUBEATLAS_OPERATIONS_PROBE_INTERVAL \
  KUBEATLAS_OPERATIONS_PROBE_TIMEOUT \
  KUBEATLAS_OPERATIONS_STALE_AFTER \
  KUBEATLAS_BACKUP_TIMESTAMP_FILE; do
  require_text "${deployment}" "${env_name}"
done
require_text "${deployment}" 'readOnly: true'
require_text "${deployment}" 'Old releases have no operations block'
require_text "${deployment}" 'default "15s"'
require_text helm/kubeatlas/values.yaml 'configMapRef:'

if grep -RiqE '^kind:[[:space:]]*(ServiceMonitor|PodMonitor|Prometheus|GrafanaDashboard)[[:space:]]*$' \
  helm/kubeatlas/templates; then
  fail 'the application chart must not install monitoring-stack CRDs'
fi

scheduled=.github/workflows/scheduled-clean-cluster.yml
require_text "${scheduled}" 'schedule:'
require_text "${scheduled}" "cron: '17 3 * * 1'"
require_text "${scheduled}" 'https://api.github.com/repos/lithastra/kubeatlas/releases/latest'
require_text "${scheduled}" 'oci://ghcr.io/lithastra/charts/kubeatlas'
require_text "${scheduled}" 'https://docs.kubeatlas.lithastra.com/${PUBLIC_VERSION}/quick-start'
require_text "${scheduled}" 'bash test/verify/public-clean-cluster.sh'
require_text "${scheduled}" 'if: always()'
require_text "${scheduled}" 'retention-days: 7'

public_verifier=test/verify/public-clean-cluster.sh
require_text "${public_verifier}" 'SECRET_SENTINEL_B64='
require_text "${public_verifier}" 'DB_PASSWORD='
require_text "${public_verifier}" 'kubeatlas_snapshot_queue_drop_total'
require_text "${public_verifier}" 'kubeatlas_graph_observation_state'
require_text "${public_verifier}" 'sensitive sentinel reached retained evidence'
require_text "${public_verifier}" 'sensitive value reached a gated surface'

tier2_workflow=.github/workflows/e2e-kind-tier2.yml
release_workflow=.github/workflows/release-preflight.yml
require_text "${tier2_workflow}" 'workflow_call:'
for minor in '1.34' '1.35' '1.36'; do
  require_text "${tier2_workflow}" "kubernetes: '${minor}'"
done
require_text "${release_workflow}" 'uses: ./.github/workflows/e2e-kind-tier2.yml'
require_text "${release_workflow}" 'name: Release preflight required results'
require_text "${release_workflow}" 'test "${CANDIDATE_RESULT}" = success'

runbook=docs/docs/operations/runbooks.md
for expected in \
  'no built-in authentication' \
  'cluster-wide information leak' \
  'Only Kubernetes Secret values' \
  'kubeatlas_graph_observation_state{state="stale"} == 1' \
  'kubeatlas_storage_reachable == 0' \
  'increase(kubeatlas_snapshot_queue_drop_total[5m]) > 0' \
  'kubeatlas_backup_age_seconds > 86400' \
  'within 120 seconds' \
  'does not install Prometheus'; do
  require_text "${runbook}" "${expected}"
done

require_text test/chaos/api-server-flap.sh 'kubeatlas_kubernetes_api_reachable 0'
require_text test/chaos/api-server-flap.sh 'within 120 s'
require_text test/chaos/pg-disconnect.sh 'kubeatlas_storage_reachable 0'
require_text test/chaos/pg-disconnect.sh 'within 120s'
require_text test/chaos/README.md '**Required PR CI**'
require_text test/chaos/README.md '**Opt-in suite**'
require_text test/chaos/README.md '**Manual**'

printf 'operability contract: metrics, deployment, evidence, and runbooks aligned\n'

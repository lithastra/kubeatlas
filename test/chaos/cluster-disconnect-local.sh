#!/usr/bin/env bash
# test/chaos/cluster-disconnect-local.sh - federation cluster-disconnect
# chaos for the local-binary fixture.
#
# Companion to cluster-disconnect.sh (the in-cluster Helm-install
# variant). This one targets the local fixture started by
# test/perf/start-multicluster-fixture.sh — kubeatlas runs as a host
# process reading kubeconfigs out of $STATE_DIR/kubeconfigs/.
#
# Scenario: with at least two member clusters attached, replace one
# member's kubeconfig with a broken endpoint, restart kubeatlas, and
# verify:
#
#   1. /healthz keeps returning 200.
#   2. /api/v1/federation/clusters still lists every member by name
#      (a broken endpoint must not silently drop the entry from the
#      attached list — partial failure is observable).
#   3. /api/v1/federation/graph?cluster=<healthy> serves the healthy
#      member's resources without the broken one poisoning the
#      response. The bench should still see the expected node count.
#
# The original kubeconfig is saved outside $STATE_DIR/kubeconfigs/
# (the loader sweeps every regular file in that directory, so a
# sibling .orig would otherwise be picked up as a third attached
# cluster) and restored on exit so the fixture is left as it was
# found.
#
# Anti-patterns guarded:
#   - Does NOT kill kind clusters. The scenario is "connection lost
#     to one member"; cluster deletion would change too much state.
#   - The trap restores the kubeconfig even on Ctrl-C.

set -euo pipefail

STATE_DIR="${STATE_DIR:-/tmp/kubeatlas-fixture}"
KUBEATLAS_PORT="${KUBEATLAS_PORT:-18080}"

for cmd in curl jq; do
  command -v "${cmd}" >/dev/null || { echo "missing: ${cmd}" >&2; exit 1; }
done

if [[ ! -d "${STATE_DIR}/kubeconfigs" ]]; then
  echo "cluster-disconnect-local: SKIPPED (no fixture at ${STATE_DIR}; run start-multicluster-fixture.sh first)" >&2
  exit 0
fi

mapfile -t members < <(ls "${STATE_DIR}/kubeconfigs/" | grep -v '^\.')
if [[ ${#members[@]} -lt 2 ]]; then
  echo "cluster-disconnect-local: SKIPPED (need >= 2 attached members, found ${#members[@]})" >&2
  exit 0
fi

victim="${members[0]}"
# Pick the first non-victim member as the healthy probe target.
healthy=""
for m in "${members[@]}"; do
  [[ "${m}" != "${victim}" ]] && { healthy="${m}"; break; }
done

# Backup OUTSIDE the kubeconfigs directory — the loader would
# otherwise treat *.orig as another attached cluster.
backup_dir="${STATE_DIR}/backup"
mkdir -p "${backup_dir}"
cp "${STATE_DIR}/kubeconfigs/${victim}" "${backup_dir}/${victim}"

restore() {
  if [[ -f "${backup_dir}/${victim}" ]]; then
    mv "${backup_dir}/${victim}" "${STATE_DIR}/kubeconfigs/${victim}"
  fi
  if [[ -f "${STATE_DIR}/kubeatlas.pid" ]]; then
    kill "$(cat ${STATE_DIR}/kubeatlas.pid)" 2>/dev/null || true
    sleep 1
  fi
  KUBEATLAS_BACKEND=memory \
  KUBEATLAS_MULTICLUSTER_ENABLED=true \
  KUBEATLAS_MULTICLUSTER_KUBECONFIG_DIR="${STATE_DIR}/kubeconfigs" \
  KUBEATLAS_API_ADDR=":${KUBEATLAS_PORT}" \
    nohup "$(dirname "${BASH_SOURCE[0]}")/../../bin/kubeatlas" \
    > "${STATE_DIR}/logs/kubeatlas.log" 2>&1 &
  echo $! > "${STATE_DIR}/kubeatlas.pid"
  for _ in $(seq 30); do
    curl -fsS "http://127.0.0.1:${KUBEATLAS_PORT}/readyz" >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "FAIL: kubeatlas did not become ready after restore" >&2
  return 1
}
trap 'restore || true' EXIT

echo "Disconnecting member '${victim}'; keeping '${healthy}' healthy"

# Replace the victim's kubeconfig with one pointing at an unreachable
# endpoint. clientcmd.RESTConfigFromKubeConfig / dynamic.NewForConfig
# are local-only operations — they succeed even with a bad endpoint —
# so the cluster attaches; its informer then fails to reach the
# apiserver. That is exactly the "connection lost to one member"
# scenario.
cat > "${STATE_DIR}/kubeconfigs/${victim}" <<'EOF'
apiVersion: v1
kind: Config
clusters:
- name: broken
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: broken
  context: {cluster: broken, user: broken}
users:
- name: broken
  user: {}
current-context: broken
EOF

# Restart kubeatlas so the multicluster manager re-reads the
# kubeconfig directory and picks up the broken endpoint.
kill "$(cat ${STATE_DIR}/kubeatlas.pid)" 2>/dev/null || true
sleep 1
KUBEATLAS_BACKEND=memory \
KUBEATLAS_MULTICLUSTER_ENABLED=true \
KUBEATLAS_MULTICLUSTER_KUBECONFIG_DIR="${STATE_DIR}/kubeconfigs" \
KUBEATLAS_API_ADDR=":${KUBEATLAS_PORT}" \
  nohup "$(dirname "${BASH_SOURCE[0]}")/../../bin/kubeatlas" \
  > "${STATE_DIR}/logs/kubeatlas-chaos.log" 2>&1 &
echo $! > "${STATE_DIR}/kubeatlas.pid"
for _ in $(seq 30); do
  curl -fsS "http://127.0.0.1:${KUBEATLAS_PORT}/readyz" >/dev/null 2>&1 && break
  sleep 1
done

base="http://127.0.0.1:${KUBEATLAS_PORT}"
healthz=$(curl -fsSL "${base}/healthz" | jq -r '.status')
if [[ "${healthz}" != "ok" ]]; then
  echo "FAIL: /healthz reports ${healthz}, expected ok" >&2
  exit 1
fi

federated=$(curl -fsSL "${base}/api/v1/federation/clusters" | jq -r '.mode')
if [[ "${federated}" != "federated" ]]; then
  echo "FAIL: /federation/clusters mode=${federated}, expected federated" >&2
  exit 1
fi

attached=$(curl -fsSL "${base}/api/v1/federation/clusters" | jq -r '.clusters | sort | @csv')
expected=$(printf '%s\n' "${members[@]}" | sort | sed 's/^\|$/"/g' | paste -sd, -)
if [[ "${attached}" != "${expected}" ]]; then
  echo "FAIL: attached=${attached} expected=${expected}" >&2
  exit 1
fi

healthy_nodes=$(curl -fsSL "${base}/api/v1/federation/graph?cluster=${healthy}" \
  | jq -r '.nodes | length')
if [[ "${healthy_nodes}" -le 0 ]]; then
  echo "FAIL: healthy member '${healthy}' returned 0 nodes; broken member should not have poisoned its result" >&2
  exit 1
fi

victim_nodes=$(curl -fsSL "${base}/api/v1/federation/graph?cluster=${victim}" \
  | jq -r '.nodes | length')

echo "PASS: federation survived '${victim}' disconnect"
echo "  /healthz: ok"
echo "  /federation/clusters: mode=federated attached=[${attached}]"
echo "  /federation/graph?cluster=${healthy}: ${healthy_nodes} nodes (still serving)"
echo "  /federation/graph?cluster=${victim}: ${victim_nodes} nodes (informer never synced — expected)"

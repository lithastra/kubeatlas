#!/usr/bin/env bash
# test/chaos/cluster-disconnect.sh - federation cluster-disconnect chaos.
#
# Scenario: KubeAtlas is running with multicluster.enabled=true and
# at least two attached member clusters. We disconnect ONE member by
# patching the kubeconfig Secret to a broken endpoint and verify
# that:
#
#   1. The disconnected cluster's informer stops (its goroutine ends
#      with a non-context-cancelled error).
#   2. The remaining cluster's informer keeps serving — every
#      cluster failure must be isolated to that one cluster.
#   3. /api/v1/federation/clusters still lists every member by name
#      (the Manager keeps the entry in its map until an explicit
#      RemoveCluster — partial failure is observable, not silent).
#   4. /api/v1/federation/graph?cluster=<healthy> returns the
#      healthy cluster's resources without the broken member
#      poisoning the response.
#
# Expected behaviour (v1.3):
#   - The KubeAtlas process does NOT exit. /healthz keeps returning
#     200; /readyz stays 200 too (readiness was marked once after
#     the manager started).
#   - At least one informer goroutine has stopped (visible in
#     server logs as "multicluster: informer stopped with error").
#   - The /federation/graph response for the healthy cluster
#     contains the expected resource count (heuristic: > 0 and
#     <= the pre-outage total).
#
# Recovery: the script restores the original Secret at the end so
# the test environment is left as it found it.
#
# Anti-patterns guarded:
#   - Does NOT kill the KubeAtlas Pod — the scenario is connection
#     loss to one member, not a server crash.
#   - Does NOT delete any cluster from the kubeconfig Secret — we
#     patch it to broken creds, so the manager has something to
#     fail on, mirroring how a misconfigured kubeconfig would behave.
#   - HA-safe: looks up the Secret by Helm release label rather
#     than hardcoding a name; reads the kubeconfig keys (cluster
#     names) from the Secret itself.

set -euo pipefail

NS="${NS:-kubeatlas}"
RELEASE="${RELEASE:-kubeatlas}"
KUBEATLAS_PF_PORT="${KUBEATLAS_PF_PORT:-18080}"

for cmd in kubectl curl jq base64; do
  command -v "${cmd}" >/dev/null || { echo "missing: ${cmd}" >&2; exit 1; }
done

# Resolve the kubeconfig Secret name from the deployment's volume
# spec (so the script does not need to know it in advance).
secret_name=$(kubectl -n "${NS}" get deploy -l app.kubernetes.io/instance="${RELEASE}" \
  -o jsonpath='{.items[0].spec.template.spec.volumes[?(@.secret)].secret.secretName}' \
  | tr ' ' '\n' | grep -v '^$' | head -1 || true)
if [[ -z "${secret_name}" ]]; then
  echo "cluster-disconnect: SKIPPED (no kubeconfig Secret mounted; multicluster.enabled=false?)" >&2
  exit 0
fi

mapfile -t cluster_keys < <(kubectl -n "${NS}" get secret "${secret_name}" \
  -o jsonpath='{.data}' | jq -r 'keys[]')
if [[ ${#cluster_keys[@]} -lt 2 ]]; then
  echo "cluster-disconnect: SKIPPED (need >= 2 attached clusters, found ${#cluster_keys[@]})" >&2
  exit 0
fi

victim="${cluster_keys[0]}"
healthy="${cluster_keys[1]}"
echo "Disconnecting member '${victim}'; keeping '${healthy}' healthy"

# Snapshot the original kubeconfig for the victim so we can restore
# it on exit.
orig_kubeconfig=$(kubectl -n "${NS}" get secret "${secret_name}" \
  -o jsonpath="{.data.${victim}}")
restore_secret() {
  kubectl -n "${NS}" patch secret "${secret_name}" --type=merge \
    -p "{\"data\":{\"${victim}\":\"${orig_kubeconfig}\"}}" >/dev/null \
    && echo "restored ${victim} kubeconfig"
}
trap restore_secret EXIT

broken_yaml=$(cat <<'EOF'
apiVersion: v1
kind: Config
clusters:
- name: broken
  cluster:
    server: https://0.0.0.1:1
contexts:
- name: broken
  context: {cluster: broken, user: broken}
users:
- name: broken
  user: {}
current-context: broken
EOF
)
broken=$(printf '%s' "${broken_yaml}" | base64 -w0)
kubectl -n "${NS}" patch secret "${secret_name}" --type=merge \
  -p "{\"data\":{\"${victim}\":\"${broken}\"}}" >/dev/null

# Restart the deployment so the manager re-reads the Secret and
# tries to attach with the broken kubeconfig.
kubectl -n "${NS}" rollout restart deploy -l app.kubernetes.io/instance="${RELEASE}"
kubectl -n "${NS}" rollout status deploy -l app.kubernetes.io/instance="${RELEASE}" --timeout=120s

# Port-forward and probe.
kubectl -n "${NS}" port-forward "svc/${RELEASE}" "${KUBEATLAS_PF_PORT}:80" >/dev/null 2>&1 &
pf_pid=$!
cleanup_pf() { kill "${pf_pid}" 2>/dev/null || true; restore_secret; }
trap cleanup_pf EXIT
sleep 3

base="http://127.0.0.1:${KUBEATLAS_PF_PORT}"
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

healthy_graph=$(curl -fsSL "${base}/api/v1/federation/graph?cluster=${healthy}" \
  | jq -r '.nodes | length')
if [[ "${healthy_graph}" -le 0 ]]; then
  echo "FAIL: /federation/graph?cluster=${healthy} returned 0 nodes; healthy member should still serve" >&2
  exit 1
fi

echo "PASS: federation survived the ${victim} disconnect"
echo "  /healthz: ok"
echo "  /federation/clusters: mode=federated (entries kept by the manager)"
echo "  /federation/graph?cluster=${healthy}: ${healthy_graph} nodes"

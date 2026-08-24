#!/usr/bin/env bash
# test/chaos/api-server-flap.sh
#
# Scenario: on Docker Desktop Kubernetes, temporarily withdraw only the
# kube-apiserver static-Pod manifest, then restore it. Validates that the
# informer survives an apiserver outage (via client-go's built-in
# exponential backoff) while continuous metrics expose degraded data.
#
# Expected behaviour (v1.6):
#   - During the outage, kubeatlas logs a stream of "watch failed"
#     errors but does NOT exit.
#   - /readyz keeps returning 200 — the informer cache is stale but
#     not invalid; serving stale graph state is acceptable while the
#     apiserver is down. (Spec choice: the chart's readiness probe
#     gates "should this Pod take traffic", not "is the cluster
#     healthy".)
#   - kubeatlas_kubernetes_api_reachable becomes 0 and graph state is
#     degraded or stale during the outage.
#   - When the apiserver returns, both metrics recover within 120 seconds.
#
# This script ONLY works on Docker Desktop's kind provisioner. It deliberately
# leaves the node container and KubeAtlas Pod running, and probes KubeAtlas
# from inside that node while kubectl and port-forwarding are unavailable.
#
# Exit code: 0 only when the outage is observed and recovery meets the budget.

set -euo pipefail

KUBEATLAS_NAMESPACE="${KUBEATLAS_NAMESPACE:-kubeatlas}"
KUBEATLAS_RELEASE="${KUBEATLAS_RELEASE:-kubeatlas}"
RESULT_FILE="${KUBEATLAS_CHAOS_RESULT_FILE:-}"
MANIFEST_PATH=/etc/kubernetes/manifests/kube-apiserver.yaml
HOLD_PATH=/var/tmp/kubeatlas-soak-kube-apiserver.yaml
MANIFEST_WITHHELD=0
RECOVERY_STARTED_AT=0
RECOVERY_SECONDS=0

cleanup() {
  if (( MANIFEST_WITHHELD == 1 )); then
    docker exec "${NODE_CONTAINER}" mv "${HOLD_PATH}" "${MANIFEST_PATH}" \
      >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

for command_name in kubectl docker curl jq grep; do
  command -v "${command_name}" >/dev/null 2>&1 || {
    echo "missing required command: ${command_name}" >&2
    exit 1
  }
done

NODE_CONTAINER="${NODE_CONTAINER:-$(kubectl get nodes -l node-role.kubernetes.io/control-plane -o jsonpath='{.items[0].metadata.name}')}"

if ! docker inspect "${NODE_CONTAINER}" >/dev/null 2>&1; then
  echo "Node container '${NODE_CONTAINER}' not found via docker." >&2
  echo "If you're on EKS/GKE/AKS, this script doesn't apply — use" >&2
  echo "the managed-service equivalent of stopping the apiserver." >&2
  exit 1
fi
[[ "$(kubectl config current-context)" == "docker-desktop" ]] || {
  echo "current context must be docker-desktop" >&2
  exit 1
}
[[ "${KUBEATLAS_CONFIRM_API_FLAP:-}" == "docker-desktop" ]] || {
  echo "set KUBEATLAS_CONFIRM_API_FLAP=docker-desktop to confirm this disruptive local-cluster drill" >&2
  exit 1
}
node_image=$(docker inspect --format '{{.Config.Image}}' "${NODE_CONTAINER}")
[[ "${node_image}" == kindest/node:* ]] || {
  echo "node container ${NODE_CONTAINER} is not a kindest/node image" >&2
  exit 1
}
docker exec "${NODE_CONTAINER}" test -f "${MANIFEST_PATH}" || {
  echo "kube-apiserver static-Pod manifest not found at ${MANIFEST_PATH}" >&2
  exit 1
}
if docker exec "${NODE_CONTAINER}" test -e "${HOLD_PATH}"; then
  echo "refusing to overwrite existing hold file ${HOLD_PATH}" >&2
  exit 1
fi
if [[ -n "${RESULT_FILE}" && -e "${RESULT_FILE}" ]]; then
  echo "refusing to overwrite existing result file ${RESULT_FILE}" >&2
  exit 1
fi

APP_POD_IP=$(kubectl get pods -n "${KUBEATLAS_NAMESPACE}" \
  -l "app.kubernetes.io/name=kubeatlas,app.kubernetes.io/instance=${KUBEATLAS_RELEASE}" \
  -o json | jq -r '[.items[] | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | first | .status.podIP // empty')
[[ -n "${APP_POD_IP}" ]] || { echo "ready KubeAtlas Pod IP not found" >&2; exit 1; }
node_api() {
  docker exec "${NODE_CONTAINER}" curl -fsS --max-time 5 \
    "http://${APP_POD_IP}:8080$1"
}
node_api /readyz >/dev/null || {
  echo "the node container cannot reach KubeAtlas Pod ${APP_POD_IP}" >&2
  exit 1
}

metrics=$(node_api /metrics)
grep -Fq 'kubeatlas_kubernetes_api_reachable 1' <<<"${metrics}" || {
  echo "Kubernetes API probe was not healthy before chaos" >&2
  exit 1
}
grep -Fq 'kubeatlas_graph_observation_state{state="synced"} 1' <<<"${metrics}" || {
  echo "graph observation state was not synced before chaos" >&2
  exit 1
}

echo "==> Snapshot of /readyz before outage"
node_api /readyz; echo

echo "==> Withholding ${MANIFEST_PATH} inside ${NODE_CONTAINER}"
MANIFEST_WITHHELD=1
docker exec "${NODE_CONTAINER}" mv "${MANIFEST_PATH}" "${HOLD_PATH}"

echo "==> Waiting up to 60 s for the dependency metrics to report the outage"
deadline=$((SECONDS + 60))
outage_observed=0
while (( SECONDS < deadline )); do
  metrics=$(node_api /metrics 2>/dev/null || true)
  if grep -Fq 'kubeatlas_kubernetes_api_reachable 0' <<<"${metrics}" &&
     { grep -Fq 'kubeatlas_graph_observation_state{state="degraded"} 1' <<<"${metrics}" ||
       grep -Fq 'kubeatlas_graph_observation_state{state="stale"} 1' <<<"${metrics}"; }; then
    outage_observed=1
    break
  fi
  sleep 2
done
(( outage_observed == 1 )) || {
  echo "operational metrics did not expose the API outage within 60 s" >&2
  exit 1
}

echo "==> /readyz during outage (expect 200; readiness gates Pod traffic, not cluster health):"
node_api /readyz; echo

echo "==> Restoring the kube-apiserver manifest"
docker exec "${NODE_CONTAINER}" mv "${HOLD_PATH}" "${MANIFEST_PATH}"
MANIFEST_WITHHELD=0
RECOVERY_STARTED_AT=$(date +%s)

echo "==> Waiting up to 120 s for the apiserver to come back"
for i in $(seq 1 120); do
  if kubectl get nodes >/dev/null 2>&1; then
    echo "apiserver responsive after ${i} s"
    break
  fi
  sleep 1
done

echo "==> Waiting up to 120 s for KubeAtlas dependency metrics to recover"
deadline=$((SECONDS + 120))
recovered=0
while (( SECONDS < deadline )); do
  metrics=$(node_api /metrics 2>/dev/null || true)
  if grep -Fq 'kubeatlas_kubernetes_api_reachable 1' <<<"${metrics}" &&
     grep -Fq 'kubeatlas_graph_observation_state{state="synced"} 1' <<<"${metrics}"; then
    recovered=1
    break
  fi
  sleep 2
done
(( recovered == 1 )) || {
  echo "KubeAtlas operational metrics did not recover within 120 s" >&2
  exit 1
}
RECOVERY_SECONDS=$(( $(date +%s) - RECOVERY_STARTED_AT ))
(( RECOVERY_SECONDS <= 120 )) || {
  echo "KubeAtlas recovered in ${RECOVERY_SECONDS}s, exceeding the 120s budget" >&2
  exit 1
}

echo "==> /readyz after recovery"
node_api /readyz; echo

if [[ -n "${RESULT_FILE}" ]]; then
  jq -n \
    --arg scenario api-server-interruption \
    --arg context docker-desktop \
    --arg node_container "${NODE_CONTAINER}" \
    --argjson recovery_seconds "${RECOVERY_SECONDS}" \
    '{scenario: $scenario, status: "pass", context: $context, node_container: $node_container, outage_observed: true, recovery_seconds: $recovery_seconds}' \
    >"${RESULT_FILE}"
fi

echo
echo "Expected: kubeatlas keeps running across the outage and serves"
echo "fresh dependency signals within 120 s of recovery. Check"
echo "kubeatlas.log for 'watch of *v1.Pod ended' / reconnect lines."

# Cleanup is implicit — we restored the apiserver. Nothing else to undo.
true

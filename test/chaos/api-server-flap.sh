#!/usr/bin/env bash
# test/chaos/api-server-flap.sh
#
# Scenario: stop the kind control-plane container, wait, then restart it.
# Validates that the
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
#   - Any resources created during the outage appear in the graph
#     within ~30 s of apiserver recovery.
#
# This script ONLY works on a kind cluster (the apiserver runs as a
# pod under kube-system on the control-plane node). On managed K8s
# (EKS/GKE/AKS) the apiserver is not directly scalable; use the
# managed-service "stop" knob instead.
#
# Exit code: 0 if kubectl operations succeed.

set -euo pipefail

CLEANUP="${CLEANUP:-1}"
[ "${1:-}" = "--no-cleanup" ] && CLEANUP=0
KUBEATLAS_METRICS_URL="${KUBEATLAS_METRICS_URL:-http://localhost:8080/metrics}"
KUBEATLAS_READYZ_URL="${KUBEATLAS_READYZ_URL:-http://localhost:8080/readyz}"
NODE_STOPPED=0

cleanup() {
  if (( NODE_STOPPED == 1 && CLEANUP == 1 )); then
    docker start "${NODE_CONTAINER}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# Detect kind: kube-apiserver runs as a static pod on the
# control-plane node, so it isn't a Deployment we can scale. We
# instead pause the kubelet on that node by docker-stop'ing the
# kind container, then docker-start it back up.
NODE_CONTAINER="${NODE_CONTAINER:-$(kubectl get nodes -l node-role.kubernetes.io/control-plane -o jsonpath='{.items[0].metadata.name}')}"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker not on PATH — required to flap the kind apiserver" >&2
  exit 1
fi

if ! docker inspect "${NODE_CONTAINER}" >/dev/null 2>&1; then
  echo "Node container '${NODE_CONTAINER}' not found via docker." >&2
  echo "If you're on EKS/GKE/AKS, this script doesn't apply — use" >&2
  echo "the managed-service equivalent of stopping the apiserver." >&2
  exit 1
fi

metrics=$(curl -fsS "${KUBEATLAS_METRICS_URL}")
grep -Fq 'kubeatlas_kubernetes_api_reachable 1' <<<"${metrics}" || {
  echo "Kubernetes API probe was not healthy before chaos" >&2
  exit 1
}
grep -Fq 'kubeatlas_graph_observation_state{state="synced"} 1' <<<"${metrics}" || {
  echo "graph observation state was not synced before chaos" >&2
  exit 1
}

echo "==> Snapshot of /readyz before outage"
curl -fsS "${KUBEATLAS_READYZ_URL}"; echo

echo "==> Stopping kind container ${NODE_CONTAINER} (apiserver goes away)"
docker stop "${NODE_CONTAINER}"
NODE_STOPPED=1

echo "==> Waiting up to 60 s for the dependency metrics to report the outage"
deadline=$((SECONDS + 60))
outage_observed=0
while (( SECONDS < deadline )); do
  metrics=$(curl -fsS "${KUBEATLAS_METRICS_URL}" 2>/dev/null || true)
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
curl -fsS "${KUBEATLAS_READYZ_URL}"; echo

echo "==> Restarting kind container"
docker start "${NODE_CONTAINER}"
NODE_STOPPED=0

echo "==> Waiting up to 60 s for the apiserver to come back"
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
  metrics=$(curl -fsS "${KUBEATLAS_METRICS_URL}" 2>/dev/null || true)
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

echo "==> /readyz after recovery"
curl -fsS "${KUBEATLAS_READYZ_URL}"; echo

echo
echo "Expected: kubeatlas keeps running across the outage and serves"
echo "fresh dependency signals within 120 s of recovery. Check"
echo "kubeatlas.log for 'watch of *v1.Pod ended' / reconnect lines."

# Cleanup is implicit — we restored the apiserver. Nothing else to undo.
true

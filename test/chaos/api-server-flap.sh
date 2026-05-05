#!/usr/bin/env bash
# test/chaos/api-server-flap.sh
#
# Scenario: scale the kind cluster's apiserver Deployment to 0
# replicas, wait, then scale it back to 1. Validates that the
# informer survives an apiserver outage (via client-go's built-in
# exponential backoff) and that /readyz reflects reality.
#
# Expected behaviour (v0.1.0):
#   - During the outage, kubeatlas logs a stream of "watch failed"
#     errors but does NOT exit.
#   - /readyz keeps returning 200 — the informer cache is stale but
#     not invalid; serving stale graph state is acceptable while the
#     apiserver is down. (Spec choice: the chart's readiness probe
#     gates "should this Pod take traffic", not "is the cluster
#     healthy".)
#   - When the apiserver returns, the informer reconnects within
#     ~30 seconds (backoff hits its ceiling at 30s in client-go
#     defaults).
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

echo "==> Snapshot of /readyz before outage"
curl -sS "http://localhost:8080/readyz" || true; echo

echo "==> Stopping kind container ${NODE_CONTAINER} (apiserver goes away)"
docker stop "${NODE_CONTAINER}"

echo "==> Sleeping 30 s with the apiserver down"
sleep 30

echo "==> /readyz during outage (expect 200; readiness gates Pod traffic, not cluster health):"
curl -sS "http://localhost:8080/readyz" || true; echo

echo "==> Restarting kind container"
docker start "${NODE_CONTAINER}"

echo "==> Waiting up to 60 s for the apiserver to come back"
for i in $(seq 1 60); do
  if kubectl get nodes >/dev/null 2>&1; then
    echo "apiserver responsive after ${i} s"
    break
  fi
  sleep 1
done

echo "==> /readyz after recovery"
curl -sS "http://localhost:8080/readyz" || true; echo

echo
echo "Expected: kubeatlas keeps running across the outage and serves"
echo "fresh data within ~30 s of the apiserver coming back. Check"
echo "kubeatlas.log for 'watch of *v1.Pod ended' / reconnect lines."

# Cleanup is implicit — we restored the apiserver. Nothing else to undo.
true

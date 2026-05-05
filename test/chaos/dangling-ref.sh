#!/usr/bin/env bash
# test/chaos/dangling-ref.sh
#
# Scenario: a Deployment references a ConfigMap; we delete the
# ConfigMap. Real clusters do this all the time when a kustomize
# overlay drops a key without bumping the Deployment.
#
# Expected behaviour (v0.1.0):
#   - The ConfigMap node disappears from the graph within ~5 seconds
#     of the kubectl delete.
#   - The USES_CONFIGMAP edge from the Deployment disappears with
#     it; pkg/store/memory.DeleteResource cascades to every incident
#     edge (see DeleteResource in pkg/store/memory/store.go).
#   - The Deployment's resource detail page shows one fewer outgoing
#     edge; there is NO "broken edge" indicator in v0.1.0.
#   - kubeatlas keeps serving without restart.
#
# Why no "broken edge" marker: tracking dangling references requires
# storing reference *intent* alongside resolved edges and surfacing
# both states. That data model lands in Phase 2 (v1.0). For v0.1.0
# the pragmatic answer is "the edge vanishes; you discover the gap
# the next time you look at the Deployment's YAML".
#
# Exit code: 0 if kubectl operations succeed (does not validate
# kubeatlas behaviour — eyeball the UI / API).

set -euo pipefail

NS="${NS:-petclinic-chaos}"
CLEANUP="${CLEANUP:-1}"

[ "${1:-}" = "--no-cleanup" ] && CLEANUP=0

echo "==> Creating chaos namespace ${NS}"
kubectl create namespace "${NS}" --dry-run=client -o yaml | kubectl apply -f -

cat <<YAML | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: dangling-cm
  namespace: ${NS}
data:
  key: value
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dangling-dep
  namespace: ${NS}
spec:
  replicas: 0
  selector:
    matchLabels: { app: dangling }
  template:
    metadata:
      labels: { app: dangling }
    spec:
      containers:
        - name: c
          image: nginx:alpine
          envFrom:
            - configMapRef:
                name: dangling-cm
YAML

echo "==> Waiting for kubeatlas to see the new resources (~5s)"
sleep 5

echo "==> Snapshot before delete:"
curl -fsS "http://localhost:8080/api/v1alpha1/resources/${NS}/Deployment/dangling-dep/outgoing" \
  | jq '.edges | map(select(.type=="USES_CONFIGMAP"))'

echo "==> Deleting ConfigMap dangling-cm"
kubectl delete configmap -n "${NS}" dangling-cm

echo "==> Waiting for the watch event to propagate (~5s)"
sleep 5

echo "==> Snapshot after delete:"
curl -fsS "http://localhost:8080/api/v1alpha1/resources/${NS}/Deployment/dangling-dep/outgoing" \
  | jq '.edges | map(select(.type=="USES_CONFIGMAP"))'

echo
echo "Expected: the second snapshot is an empty array (edge cascaded)."
echo "If you see a 'broken edge' indicator: file an issue (v0.1.0 doesn't ship one)."

if [ "${CLEANUP}" = "1" ]; then
  echo "==> Cleaning up namespace ${NS}"
  kubectl delete namespace "${NS}" --ignore-not-found
fi

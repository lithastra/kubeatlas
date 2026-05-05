#!/usr/bin/env bash
# test/chaos/owner-loop.sh
#
# Scenario: two resources name each other in metadata.ownerReferences.
# The K8s API server permits this (controllers don't validate
# ownerRefs across creation order); KubeAtlas's OwnerRef walker must
# not infinite-loop on it.
#
# Expected behaviour (v0.1.0):
#   - Both ConfigMaps appear as distinct nodes.
#   - Two OWNS edges exist (cm-a -> cm-b and cm-b -> cm-a, since
#     OWNS is owned->owner).
#   - The workload aggregator's BFS uses a visited-set keyed on UID,
#     so a request like
#       /api/v1alpha1/graph?level=workload&namespace=${NS}&kind=ConfigMap&name=cm-a
#     completes in milliseconds with two nodes returned, NOT a hang.
#   - kubeatlas's RSS does not balloon (no edge or node duplication).
#
# Why this matters: the OwnerRef walker is the only piece of
# kubeatlas with cycle exposure. Every other extractor produces a
# DAG slice (USES_*, MOUNTS_*, SELECTS, etc.).
#
# Exit code: 0 if kubectl + curl succeed.

set -euo pipefail

NS="${NS:-petclinic-chaos}"
CLEANUP="${CLEANUP:-1}"

[ "${1:-}" = "--no-cleanup" ] && CLEANUP=0

echo "==> Creating chaos namespace ${NS}"
kubectl create namespace "${NS}" --dry-run=client -o yaml | kubectl apply -f -

# Create cm-a first WITHOUT an ownerRef. Capture its UID.
kubectl apply -n "${NS}" -f - <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm-a
data: { k: a }
YAML

UID_A=$(kubectl get -n "${NS}" cm cm-a -o jsonpath='{.metadata.uid}')

# Now create cm-b owning cm-a, and capture its UID.
kubectl apply -n "${NS}" -f - <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm-b
  ownerReferences:
    - apiVersion: v1
      kind: ConfigMap
      name: cm-a
      uid: ${UID_A}
data: { k: b }
YAML

UID_B=$(kubectl get -n "${NS}" cm cm-b -o jsonpath='{.metadata.uid}')

# Patch cm-a to claim cm-b as its owner — closes the loop.
kubectl patch -n "${NS}" cm cm-a --type=merge -p "$(cat <<JSON
{
  "metadata": {
    "ownerReferences": [
      { "apiVersion": "v1", "kind": "ConfigMap", "name": "cm-b", "uid": "${UID_B}" }
    ]
  }
}
JSON
)"

echo "==> Waiting for both ownerRefs to propagate (~5s)"
sleep 5

echo "==> Walking workload aggregator from cm-a (must terminate)"
time curl -fsS \
  "http://localhost:8080/api/v1alpha1/graph?level=workload&namespace=${NS}&kind=ConfigMap&name=cm-a" \
  | jq '{nodes_count: (.nodes|length), edges_count: (.edges|length)}'

echo
echo "Expected: 2 nodes, 2 edges, request completes in <100 ms."
echo "If the request hangs: cycle-detection regression (file an issue)."

if [ "${CLEANUP}" = "1" ]; then
  echo "==> Cleaning up namespace ${NS}"
  kubectl delete namespace "${NS}" --ignore-not-found
fi

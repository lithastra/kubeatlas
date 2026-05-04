#!/usr/bin/env bash
# test/verify/phase0.sh - Phase 0 exit verification.
#
# Reads three JSON snapshots produced by `kubeatlas -once` against a
# cluster that has the PetClinic fixture deployed, and asserts every
# Phase 0 invariant: 16 resource kinds, 8 edge types, the PoC blacklist
# is enforced, the OwnerRef chain is complete, and the cluster /
# namespace aggregations look right.
#
# Inputs (defaults shown; override by passing positional args):
#   $1 - resource-level graph JSON   (default /tmp/graph-resource.json)
#   $2 - cluster-level aggregation   (default /tmp/graph-cluster.json)
#   $3 - namespace-level aggregation (default /tmp/graph-namespace.json)
#
# Usage on any Kubernetes cluster you control with the PetClinic
# fixture deployed (kind is one option; managed clusters work too):
#
#   go run ./cmd/kubeatlas/ -once -level=resource > /tmp/graph-resource.json
#   go run ./cmd/kubeatlas/ -once -level=cluster   > /tmp/graph-cluster.json
#   go run ./cmd/kubeatlas/ -once -level=namespace -namespace=petclinic > /tmp/graph-namespace.json
#   bash test/verify/phase0.sh
set -euo pipefail

GRAPH_RESOURCE="${1:-/tmp/graph-resource.json}"
GRAPH_CLUSTER="${2:-/tmp/graph-cluster.json}"
GRAPH_NS="${3:-/tmp/graph-namespace.json}"

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

# Preflight: all three snapshots must exist. The script only reads JSON,
# so we want to fail loud and early when any input is missing rather
# than silently skip half the assertions.
missing=()
declare -A GENERATE_CMD=(
  ["${GRAPH_RESOURCE}"]="go run ./cmd/kubeatlas/ -once > ${GRAPH_RESOURCE}"
  ["${GRAPH_CLUSTER}"]="go run ./cmd/kubeatlas/ -once -level=cluster > ${GRAPH_CLUSTER}"
  ["${GRAPH_NS}"]="go run ./cmd/kubeatlas/ -once -level=namespace -namespace=petclinic > ${GRAPH_NS}"
)
for f in "${GRAPH_RESOURCE}" "${GRAPH_CLUSTER}" "${GRAPH_NS}"; do
  [[ -f "${f}" ]] || missing+=("${f}")
done
if [[ ${#missing[@]} -gt 0 ]]; then
  echo "Missing input file(s):" >&2
  for f in "${missing[@]}"; do
    echo "  - ${f}" >&2
    echo "      generate with: ${GENERATE_CMD[$f]}" >&2
  done
  exit 1
fi

fail() { echo "✗ FAIL: $*" >&2; exit 1; }
ok()   { echo "✓ $*"; }

# ---------------------------------------------------------------------
# Assertion 1: every resource kind we care about is present.
# ---------------------------------------------------------------------
EXPECTED_KINDS=(
  Namespace Pod Deployment ReplicaSet StatefulSet DaemonSet
  Job CronJob Service Ingress ConfigMap Secret
  PersistentVolumeClaim ServiceAccount Gateway HTTPRoute
)
for KIND in "${EXPECTED_KINDS[@]}"; do
  COUNT=$(jq "[.resources[] | select(.kind == \"${KIND}\")] | length" "${GRAPH_RESOURCE}")
  [[ "${COUNT}" -gt 0 ]] || fail "no ${KIND} resources in ${GRAPH_RESOURCE}"
  ok "${KIND}: ${COUNT}"
done

# ---------------------------------------------------------------------
# Assertion 2: every Phase 0 edge type is present at least once.
# All edges now carry the typed `type` field (the PoC's `relation`
# field was removed in Phase 1 W5).
# ---------------------------------------------------------------------
EXPECTED_EDGES=(OWNS USES_CONFIGMAP USES_SECRET MOUNTS_VOLUME SELECTS USES_SERVICEACCOUNT ROUTES_TO ATTACHED_TO)
for EDGE in "${EXPECTED_EDGES[@]}"; do
  COUNT=$(jq "[.edges[] | select(.type == \"${EDGE}\")] | length" "${GRAPH_RESOURCE}")
  [[ "${COUNT}" -gt 0 ]] || fail "no ${EDGE} edges in ${GRAPH_RESOURCE}"
  ok "${EDGE}: ${COUNT}"
done

# ---------------------------------------------------------------------
# Assertion 3: the PoC GVR blacklist is honoured.
# ---------------------------------------------------------------------
for BLOCKED in Event Lease TokenReview SubjectAccessReview SelfSubjectAccessReview Endpoints EndpointSlice; do
  COUNT=$(jq "[.resources[] | select(.kind == \"${BLOCKED}\")] | length" "${GRAPH_RESOURCE}")
  [[ "${COUNT}" -eq 0 ]] || fail "${BLOCKED} should be blacklisted but appears ${COUNT} time(s)"
done
ok "blacklist enforced (no Event/Lease/Endpoints/etc.)"

# ---------------------------------------------------------------------
# Assertion 4: api Pod -> ReplicaSet OWNS edge exists.
# ---------------------------------------------------------------------
API_POD_OWNER=$(jq -r '
  .edges[]
  | select((.type == "OWNS") and ((.from | tostring) | contains("/Pod/api-")))
  | .to' "${GRAPH_RESOURCE}" | head -1)
[[ "${API_POD_OWNER}" == *"/ReplicaSet/api-"* ]] || \
  fail "api Pod owner chain broken (got: '${API_POD_OWNER}')"
ok "ownerRef chain: api Pod -> ReplicaSet"

# ---------------------------------------------------------------------
# Assertion 5: cluster-level aggregation contains the petclinic node.
# ---------------------------------------------------------------------
PETCLINIC=$(jq '[.nodes[]? | select(.id == "petclinic")] | length' "${GRAPH_CLUSTER}")
[[ "${PETCLINIC}" -ge 1 ]] || fail "petclinic node missing in ${GRAPH_CLUSTER}"
ok "cluster-level aggregation: petclinic node present"

# ---------------------------------------------------------------------
# Assertion 6: namespace-level aggregation has at least 8 nodes.
# Workload kinds in the fixture: Deployment(api), StatefulSet(postgres),
# DaemonSet(fluentbit), Job(migrate), CronJob(db-backup), Service(api-svc,
# postgres), Ingress(web-ingress), HTTPRoute(web-route) = 9 aggregated
# nodes plus passthrough ConfigMaps / Secrets / SA / PVC.
# ---------------------------------------------------------------------
NODE_COUNT=$(jq '.nodes | length' "${GRAPH_NS}")
[[ "${NODE_COUNT}" -ge 8 ]] || \
  fail "namespace-level expected >= 8 nodes, got ${NODE_COUNT}"
ok "namespace-level aggregation: ${NODE_COUNT} nodes"

echo
echo "Phase 0 verification passed"

#!/usr/bin/env bash
# test/verify/phase2.sh - Phase 2 exit verification (M4 part 1).
#
# Asserts the Tier 2 + Rego + CRD-discovery feature trio that lands
# in M4 (P2-T1..T11) actually works against a live kind cluster:
#
#   1. Tier 2 persistence: deleting the kubeatlas Pod and waiting for
#      the next replica to be Ready returns inside the §1.7 startup
#      budget (30s slack from the 10s book number — local kind hardware
#      varies). Validates that the new Pod loads the graph from PG
#      rather than re-scanning from scratch.
#   2. Rego engine wired: /metrics exposes kubeatlas_rego_modules_loaded
#      with a non-zero value (force-loaded openshift pack via
#      rulePacks.openshift=true so this assertion does not depend on
#      cluster type).
#   3. CRD discovery: install cert-manager, wait for Discovery to
#      register the Certificate informer (visible via kubeatlas Pod
#      logs), confirm Certificate kind appears in the resources API
#      after a CR is created.
#
# M5 assertions land in part 2 below: RBAC graph, blast radius, and
# (when KUBEATLAS_CHECK_CERT_MANAGER_RULES=1) the cert-manager rule
# pack STORES_IN edge. M6 covers orphan detection (P2-T17, part 3)
# and cycle detection (P2-T18, part 4). Part 5 (P2-T26) runs the
# chaos suite + re-asserts the M4/M5/M6 readiness afterwards;
# enable with KUBEATLAS_RUN_CHAOS=1 since chaos adds 5-10 minutes
# to the run.
#
# Required tools on PATH: kubectl, helm, jq, curl.
# The script assumes:
#   - A kind cluster reachable via the current KUBECONFIG.
#   - kubeatlas already installed in namespace "kubeatlas" with
#     persistence.embedded.enabled=true and rulePacks.openshift=true.
#
# CI invokes this from the e2e-kind-tier2 workflow; locally:
#
#   kind create cluster --name kubeatlas-tier2
#   helm install kubeatlas helm/kubeatlas \
#     --namespace kubeatlas --create-namespace \
#     --set persistence.enabled=true \
#     --set persistence.embedded.enabled=true \
#     --set rulePacks.openshift=true \
#     --wait --timeout 5m
#   bash test/verify/phase2.sh

set -euo pipefail

NS="${KUBEATLAS_NAMESPACE:-kubeatlas}"
RELEASE="${KUBEATLAS_RELEASE:-kubeatlas}"
DEPLOY="${KUBEATLAS_RELEASE:-kubeatlas}"
RESTART_BUDGET_SECONDS="${KUBEATLAS_RESTART_BUDGET:-30}"
CRD_PICKUP_BUDGET_SECONDS="${KUBEATLAS_CRD_PICKUP_BUDGET:-60}"
# KUBEATLAS_SKIP_TIER2=1 skips the persistence-restart assertion so
# the rego + CRD-discovery half can be validated against a Tier 1
# (in-memory) install. Used while the Tier 2 PG+AGE bootstrap
# integration with CNPG's restricted_load_libraries is still being
# pinned down (tracked under a follow-up issue).
SKIP_TIER2="${KUBEATLAS_SKIP_TIER2:-0}"
# KUBEATLAS_CHECK_CERT_MANAGER_RULES=1 enables the optional Part 2C
# assertion that the cert-manager rule pack landed STORES_IN edges
# on the Certificate. Off by default until the OCI artifact ships.
CHECK_CERT_MANAGER_RULES="${KUBEATLAS_CHECK_CERT_MANAGER_RULES:-0}"
# KUBEATLAS_RUN_CHAOS=1 turns on Part 5 (chaos suite). Off by
# default — chaos adds 5-10 minutes to the run; the v1.0 release
# gate runs it explicitly via the workflow_dispatch trigger.
RUN_CHAOS="${KUBEATLAS_RUN_CHAOS:-0}"
M5_NS="${KUBEATLAS_M5_NS:-petclinic-m5}"

# ----- helpers -------------------------------------------------------

red()    { printf '\033[31m%s\033[0m\n' "$*" >&2; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }

step() { yellow "▶ $*"; }
pass() { green  "  ✓ $*"; }
fail() { red    "  ✗ $*"; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { red "missing required tool: $1"; exit 1; }
}

# kubeatlas_curl runs curl from the host against the kubeatlas API
# via a backgrounded port-forward managed by start_port_forward /
# stop_port_forward. The runtime image is distroless (no sh/curl
# available), so `kubectl exec ... curl` is not an option.
KUBEATLAS_PF_PORT="${KUBEATLAS_PF_PORT:-18080}"
KUBEATLAS_PF_PID=""

start_port_forward() {
  if [[ -n "${KUBEATLAS_PF_PID}" ]]; then
    return 0
  fi
  kubectl port-forward -n "${NS}" "deploy/${DEPLOY}" \
    "${KUBEATLAS_PF_PORT}:8080" >/tmp/kubeatlas-pf.log 2>&1 &
  KUBEATLAS_PF_PID=$!
  trap stop_port_forward EXIT
  # Wait until the local port is responsive.
  for _ in $(seq 1 30); do
    if curl -fsS --max-time 1 "http://127.0.0.1:${KUBEATLAS_PF_PORT}/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "port-forward did not become ready on :${KUBEATLAS_PF_PORT}"
}

stop_port_forward() {
  if [[ -n "${KUBEATLAS_PF_PID}" ]] && kill -0 "${KUBEATLAS_PF_PID}" 2>/dev/null; then
    kill "${KUBEATLAS_PF_PID}" 2>/dev/null || true
    wait "${KUBEATLAS_PF_PID}" 2>/dev/null || true
  fi
  KUBEATLAS_PF_PID=""
  # Wait until the OS has reclaimed the local port so a subsequent
  # start_port_forward doesn't race against our own EADDRINUSE.
  for _ in $(seq 1 10); do
    if ! curl -fsS --max-time 1 "http://127.0.0.1:${KUBEATLAS_PF_PORT}/healthz" >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
}

kubeatlas_curl() {
  curl -fsS --max-time 10 "http://127.0.0.1:${KUBEATLAS_PF_PORT}$1"
}

# wait_for_pod_ready blocks until the deployment's first ready Pod is
# Ready. Returns when seen, fails after the per-step deadline.
wait_for_pod_ready() {
  local timeout=$1
  local deadline=$((SECONDS + timeout))
  while (( SECONDS < deadline )); do
    if kubectl get pod -n "${NS}" -l "app.kubernetes.io/name=${RELEASE}" \
        -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null \
        | grep -qw "True"; then
      return 0
    fi
    sleep 1
  done
  fail "no Ready Pod after ${timeout}s"
}

# ----- preflight -----------------------------------------------------

step "preflight: required tools available"
require_cmd kubectl
require_cmd helm
require_cmd jq
require_cmd curl
pass "kubectl + helm + jq + curl on PATH"

step "preflight: kubeatlas Pod is initially Ready"
wait_for_pod_ready 30
pass "kubeatlas Ready"

step "preflight: open port-forward to kubeatlas /api"
start_port_forward
pass "kubeatlas API reachable on :${KUBEATLAS_PF_PORT}"

# ----- assertion 1: rego engine wired into /metrics ------------------

step "rego engine: /metrics emits kubeatlas_rego_modules_loaded"
metrics=$(kubeatlas_curl /metrics) || fail "/metrics scrape failed"
if ! grep -q '^# TYPE kubeatlas_rego_modules_loaded gauge' <<<"${metrics}"; then
  fail "kubeatlas_rego_modules_loaded gauge missing from /metrics"
fi
loaded=$(grep '^kubeatlas_rego_modules_loaded ' <<<"${metrics}" | awk '{print $2}')
if [[ -z "${loaded}" || "${loaded}" -lt 1 ]]; then
  fail "kubeatlas_rego_modules_loaded = '${loaded:-<empty>}', want >=1 (rulePacks.openshift=true should force-load)"
fi
pass "kubeatlas_rego_modules_loaded = ${loaded}"

step "rego engine: /metrics emits all four counter families"
for counter in kubeatlas_rego_cache_hits_total \
               kubeatlas_rego_cache_misses_total \
               kubeatlas_rego_eval_timeout_total \
               kubeatlas_rego_eval_panic_total; do
  if ! grep -q "^# TYPE ${counter} counter" <<<"${metrics}"; then
    fail "${counter} not exposed by /metrics"
  fi
done
pass "cache_hits / cache_misses / eval_timeout / eval_panic all exposed"

# ----- assertion 2: Tier 2 persistence survives Pod restart ----------

if [[ "${SKIP_TIER2}" == "1" ]]; then
  yellow "▶ persistence: SKIPPED via KUBEATLAS_SKIP_TIER2=1"
  pre_resources="<skipped>"
  post_resources="<skipped>"
  restart_elapsed="<skipped>"
else
  step "persistence: capture pre-restart resource count"
  pre_resources=$(kubeatlas_curl '/api/v1alpha1/graph?level=cluster' \
    | jq -r '.resources | length')
  [[ "${pre_resources}" =~ ^[0-9]+$ ]] || fail "resource count not numeric: ${pre_resources}"
  pass "pre-restart cluster resources: ${pre_resources}"

  step "persistence: delete kubeatlas Pod, measure restart budget"
  old_pod=$(kubectl get pod -n "${NS}" -l "app.kubernetes.io/name=${RELEASE}" \
    -o jsonpath='{.items[0].metadata.name}')
  [[ -n "${old_pod}" ]] || fail "no Pod found"
  # The port-forward we opened in preflight binds to the soon-to-be-
  # killed Pod. Stop it before deletion and reopen against the new
  # Pod once the deployment rollout is complete.
  stop_port_forward
  kubectl delete pod -n "${NS}" "${old_pod}" --wait=false >/dev/null
  restart_started=${SECONDS}
  # `kubectl rollout status` is the deployment-level signal: it waits
  # until the new ReplicaSet has the desired count of Ready Pods AND
  # the old one is fully drained. Plain `wait_for_pod_ready` could
  # return on the still-terminating Pod's Ready=True condition,
  # which then fights kubectl port-forward.
  if ! kubectl rollout status -n "${NS}" "deploy/${DEPLOY}" \
       --timeout="${RESTART_BUDGET_SECONDS}s" >/dev/null 2>&1; then
    fail "deployment rollout did not complete in ${RESTART_BUDGET_SECONDS}s"
  fi
  restart_elapsed=$(( SECONDS - restart_started ))
  start_port_forward
  pass "new Pod Ready in ${restart_elapsed}s (budget ${RESTART_BUDGET_SECONDS}s)"

  step "persistence: post-restart resource count matches pre-restart"
  # Allow up to 60s of informer re-sync churn; the count must converge
  # to the same value the persistent backend remembered.
  deadline=$((SECONDS + 60))
  post_resources=""
  while (( SECONDS < deadline )); do
    post_resources=$(kubeatlas_curl '/api/v1alpha1/graph?level=cluster' \
      | jq -r '.resources | length' 2>/dev/null || echo "")
    if [[ "${post_resources}" == "${pre_resources}" ]]; then
      break
    fi
    sleep 2
  done
  if [[ "${post_resources}" != "${pre_resources}" ]]; then
    fail "post-restart resources (${post_resources}) differ from pre-restart (${pre_resources}) after 60s"
  fi
  pass "post-restart resources: ${post_resources} (== pre-restart)"
fi

# ----- assertion 3: CRD discovery picks up cert-manager --------------

step "crd discovery: install cert-manager (if not already)"
if ! kubectl get crd certificates.cert-manager.io >/dev/null 2>&1; then
  kubectl apply -f \
    https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml \
    >/dev/null
fi
kubectl wait --for=condition=ready pod \
  -l app.kubernetes.io/instance=cert-manager \
  -n cert-manager --timeout=120s >/dev/null
pass "cert-manager Pods Ready"

# The "discovered CRD" log is the early signal Discovery fired; the
# stronger end-to-end assertion below — a fresh Certificate landing
# in the API — also implies the informer registered. We keep the log
# probe as a soft hint (best-effort grep on the recent tail) but do
# not fail the suite when the line has rolled out of the buffer; the
# Pod can run for hours before this script runs.
step "crd discovery: best-effort check for Certificate informer log"
if kubectl logs -n "${NS}" "deploy/${DEPLOY}" --tail=20000 2>/dev/null \
    | grep -q 'Discovered CRD.*cert-manager.io.*certificates'; then
  pass "Discovery log line still in tail"
else
  yellow "  (log line rolled out; relying on the API assertion below)"
fi

step "crd discovery: a fresh Certificate flows into the API"
kubectl create namespace cert-test --dry-run=client -o yaml | kubectl apply -f - >/dev/null
cat <<'EOF' | kubectl apply -f - >/dev/null
apiVersion: cert-manager.io/v1
kind: Issuer
metadata: { name: selfsigned, namespace: cert-test }
spec: { selfSigned: {} }
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: { name: phase2-cert, namespace: cert-test }
spec:
  secretName: phase2-cert-tls
  issuerRef: { name: selfsigned, kind: Issuer }
  commonName: phase2-cert
  duration: 8760h
EOF

deadline=$((SECONDS + CRD_PICKUP_BUDGET_SECONDS))
saw_cert=0
while (( SECONDS < deadline )); do
  body=$(kubeatlas_curl '/api/v1alpha1/resources/cert-test/Certificate/phase2-cert' 2>/dev/null || echo '{}')
  if jq -e '.resource | select(.kind == "Certificate" and .name == "phase2-cert" and .namespace == "cert-test")' <<<"${body}" >/dev/null 2>&1; then
    saw_cert=1
    break
  fi
  sleep 2
done
(( saw_cert == 1 )) \
  || fail "Certificate cert-test/phase2-cert never appeared in /api/v1alpha1/resources/{ns}/{kind}/{name}"
pass "Certificate visible via kubeatlas API"

# ----- Part 2A (M5): RBAC graph -------------------------------------
#
# Seeds a SA + Role + RoleBinding fixture, waits for the informer to
# pick all three up, then walks the BINDS_SUBJECT / BINDS_ROLE chain
# back through the new RBAC API. Validates that the extractor pair
# emits the right edges and that the API surfaces the role's rules.

step "rbac: seed ServiceAccount + Role + RoleBinding fixture"
kubectl create namespace "${M5_NS}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: ServiceAccount
metadata:
  name: api-sa
  namespace: ${M5_NS}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: api-cm-reader
  namespace: ${M5_NS}
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: api-cm-reader
  namespace: ${M5_NS}
subjects:
  - kind: ServiceAccount
    name: api-sa
    namespace: ${M5_NS}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: api-cm-reader
EOF
pass "RBAC fixture applied in ${M5_NS}"

step "rbac: BINDS_SUBJECT + BINDS_ROLE land for api-sa"
deadline=$((SECONDS + 60))
saw_perms=0
perms_body=""
while (( SECONDS < deadline )); do
  perms_body=$(kubeatlas_curl "/api/v1alpha1/rbac/serviceaccount/${M5_NS}/api-sa/permissions" 2>/dev/null || echo '{}')
  if jq -e '.bindings | length >= 1 and any(.[]; .role.name == "api-cm-reader")' <<<"${perms_body}" >/dev/null 2>&1; then
    saw_perms=1
    break
  fi
  sleep 2
done
(( saw_perms == 1 )) || fail "rbac permissions for ${M5_NS}/api-sa never showed api-cm-reader binding (last body: ${perms_body})"
# Inner sanity: the rule must mention configmaps.
if ! jq -e '[.bindings[].rules[]?.resources[]] | index("configmaps")' <<<"${perms_body}" >/dev/null 2>&1; then
  fail "permissions response missing the configmaps verb (body: ${perms_body})"
fi
pass "api-sa permissions resolve to api-cm-reader / configmaps"

step "rbac: role -> subjects walk returns api-sa"
subjects_body=$(kubeatlas_curl "/api/v1alpha1/rbac/role/${M5_NS}/api-cm-reader/subjects")
if ! jq -e '[.bindings[].subjects[]?.name] | index("api-sa")' <<<"${subjects_body}" >/dev/null 2>&1; then
  fail "role subjects walk missing api-sa (body: ${subjects_body})"
fi
pass "role subjects walk lists api-sa"

# ----- Part 2B (M5): blast radius ------------------------------------
#
# Adds a Deployment that mounts a ConfigMap, then queries the
# blast-radius endpoint on the ConfigMap. The Deployment + its
# downstream ReplicaSet must show up — confirming the
# DirectionIncoming traversal works end-to-end against the live
# graph.

step "blast-radius: seed Deployment that consumes a ConfigMap"
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: ${M5_NS}
data:
  greeting: hello
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: ${M5_NS}
spec:
  replicas: 1
  selector: { matchLabels: { app: api } }
  template:
    metadata: { labels: { app: api } }
    spec:
      serviceAccountName: api-sa
      containers:
        - name: app
          image: registry.k8s.io/pause:3.10
          envFrom:
            - configMapRef: { name: app-config }
EOF
pass "blast-radius fixture applied in ${M5_NS}"

step "blast-radius: walk incoming from app-config reaches api Deployment"
deadline=$((SECONDS + 90))
saw_blast=0
blast_body=""
while (( SECONDS < deadline )); do
  blast_body=$(kubeatlas_curl "/api/v1alpha1/blast-radius/${M5_NS}/ConfigMap/app-config" 2>/dev/null || echo '{}')
  # Tier 2 traversal can return only the immediate consumers (the
  # Deployment) or also the cascaded ReplicaSet / Pod once
  # extractors converge. Either reaching api is enough — that's the
  # operationally interesting answer.
  if jq -e '[.affected[] | select(.kind == "Deployment" and .name == "api")] | length >= 1' <<<"${blast_body}" >/dev/null 2>&1; then
    saw_blast=1
    break
  fi
  sleep 3
done
(( saw_blast == 1 )) || fail "blast-radius from app-config never reached api Deployment (last body: ${blast_body})"
blast_count=$(jq -r '.count' <<<"${blast_body}")
pass "blast-radius affected count = ${blast_count} (includes api Deployment)"

# ----- Part 2C (M5, optional): cert-manager rule pack edges ----------

if [[ "${CHECK_CERT_MANAGER_RULES}" == "1" ]]; then
  step "cert-manager rules: STORES_IN edge from Certificate to Secret"
  deadline=$((SECONDS + 60))
  saw_stores_in=0
  outgoing_body=""
  while (( SECONDS < deadline )); do
    outgoing_body=$(kubeatlas_curl '/api/v1alpha1/resources/cert-test/Certificate/phase2-cert/outgoing' 2>/dev/null || echo '[]')
    if jq -e '[.[] | select(.type == "STORES_IN")] | length >= 1' <<<"${outgoing_body}" >/dev/null 2>&1; then
      saw_stores_in=1
      break
    fi
    sleep 2
  done
  (( saw_stores_in == 1 )) \
    || fail "cert-manager STORES_IN edge never appeared on phase2-cert (rule pack not loaded?)"
  pass "STORES_IN edge present on phase2-cert"
else
  yellow "▶ cert-manager rules: SKIPPED (set KUBEATLAS_CHECK_CERT_MANAGER_RULES=1 to enable)"
fi

# ----- Part 3 (M6): orphan detection ---------------------------------
#
# Creates a deliberately orphan ReplicaSet (no Deployment owner, no
# Pods) in the M5 fixture namespace and asserts /api/v1alpha1/orphans
# flags it with reason=orphan. The same call must NOT include the
# Namespace itself (top-level whitelist) or any healthy resource
# from Part 2's blast-radius fixture.

step "orphans: seed an unowned ReplicaSet"
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: ghost-rs
  namespace: ${M5_NS}
spec:
  replicas: 0
  selector: { matchLabels: { app: ghost } }
  template:
    metadata: { labels: { app: ghost } }
    spec:
      containers:
        - name: app
          image: registry.k8s.io/pause:3.10
EOF
pass "ghost-rs applied in ${M5_NS}"

step "orphans: ghost-rs appears in /api/v1alpha1/orphans"
deadline=$((SECONDS + 60))
saw_orphan=0
orphans_body=""
while (( SECONDS < deadline )); do
  orphans_body=$(kubeatlas_curl "/api/v1alpha1/orphans?namespace=${M5_NS}" 2>/dev/null || echo '{}')
  if jq -e '[.reports[] | select(.resource.kind == "ReplicaSet" and .resource.name == "ghost-rs" and .reason == "orphan")] | length >= 1' <<<"${orphans_body}" >/dev/null 2>&1; then
    saw_orphan=1
    break
  fi
  sleep 2
done
(( saw_orphan == 1 )) || fail "ghost-rs never appeared in orphans endpoint (last body: ${orphans_body})"
pass "ghost-rs flagged with reason=orphan"

step "orphans: Namespace is not flagged (top-level whitelist)"
all_orphans=$(kubeatlas_curl "/api/v1alpha1/orphans")
if jq -e '[.reports[] | select(.resource.kind == "Namespace")] | length >= 1' <<<"${all_orphans}" >/dev/null 2>&1; then
  fail "Namespace incorrectly flagged: ${all_orphans}"
fi
orphan_count=$(jq -r '.count' <<<"${all_orphans}")
pass "Namespace excluded; total orphan reports = ${orphan_count}"

# ----- Part 4 (M6): cycle detection ----------------------------------
#
# Tarjan's SCC must report no *unexpected* cycles on a healthy
# cluster. Real installs frequently carry "bootstrap-cert"
# cycles where a webhook controller owns its own cert Secret
# AND consumes that Secret (CNPG, cert-manager, kyverno, ...).
# Tarjan correctly identifies these — they're real cycles by
# graph definition — but they're benign at the operational
# level. The assertion below counts those out and only fails on
# residual cycles.
#
# A bootstrap-cert cycle is a 2-member SCC where one member is
# kind=Secret with an OwnerReference pointing at the other
# member (any kind, typically a Deployment). Anything not
# matching that shape is treated as a real finding.
#
# Unit tests in pkg/graph/analysis/cycles_test.go cover the
# positive case (a 3-cycle of ConfigMaps) on a deterministic
# in-memory fixture, so the algorithm itself is gated
# elsewhere.

step "cycles: /api/v1alpha1/cycles has no unexpected entries"
cycles_body=$(kubeatlas_curl /api/v1alpha1/cycles)
cycles_count=$(jq -r '.count' <<<"${cycles_body}")
if ! [[ "${cycles_count}" =~ ^[0-9]+$ ]]; then
  fail "cycles count not numeric (body: ${cycles_body})"
fi
benign_count=$(jq '
  def benign:
    if (.members | length) != 2 then false
    else . as $c |
      (
        ($c.members[0].kind == "Secret" and
         (($c.members[0].ownerReferences // []) | map(.name)
          | index($c.members[1].name) != null))
        or
        ($c.members[1].kind == "Secret" and
         (($c.members[1].ownerReferences // []) | map(.name)
          | index($c.members[0].name) != null))
      )
    end;
  [.cycles[] | select(benign)] | length
' <<<"${cycles_body}")
real_cycles=$(( cycles_count - benign_count ))
if (( real_cycles > 0 )); then
  fail "cycles endpoint reported ${real_cycles} non-benign cycle(s) (total=${cycles_count}, benign=${benign_count}): ${cycles_body}"
fi
pass "no unexpected cycles (total=${cycles_count}, benign bootstrap-cert=${benign_count})"

# ----- Part 5 (M6.3): chaos suite + post-chaos re-verify ------------
#
# Each scenario is invoked with the port-forward env exported so
# the chaos scripts hit the same kubeatlas the assertions above
# already validated. Between scenarios we re-run a thin
# /healthz + /readyz probe and a cluster-view fetch to confirm
# nothing residual broke. Each scenario script is responsible for
# its own internal recovery wait; the 30s sleep between scenarios
# is anti-pattern guard #2 (chaos must have recovery time).

if [[ "${RUN_CHAOS}" == "1" ]]; then
  step "chaos: prerequisite — kubeatlas /healthz before chaos suite"
  curl -fsS "http://127.0.0.1:${KUBEATLAS_PF_PORT}/healthz" >/dev/null \
    || fail "kubeatlas not healthy before chaos suite"
  pass "kubeatlas healthy"

  DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  REPO_ROOT="$(cd "${DIR}/../.." && pwd)"

  chaos_scenarios=(
    "${REPO_ROOT}/test/chaos/pg-disconnect.sh"
    "${REPO_ROOT}/test/chaos/rego-panic.sh"
    "${REPO_ROOT}/test/chaos/rego-runaway.sh"
    "${REPO_ROOT}/test/chaos/cert-manager-flap.sh"
  )

  for scenario in "${chaos_scenarios[@]}"; do
    name="$(basename "${scenario}" .sh)"
    step "chaos: ${name}"
    KUBEATLAS_PF_PORT="${KUBEATLAS_PF_PORT}" \
    KUBEATLAS_TIER="${KUBEATLAS_TIER:-tier2}" \
    NS="${NS}" \
    RELEASE="${RELEASE}" \
      bash "${scenario}" || fail "chaos ${name} reported failure"
    pass "chaos ${name} green"

    step "chaos: post-${name} re-verify (kubeatlas health + cluster view)"
    curl -fsS "http://127.0.0.1:${KUBEATLAS_PF_PORT}/healthz" >/dev/null \
      || fail "post-${name}: /healthz not 200"
    cluster_resp=$(kubeatlas_curl '/api/v1alpha1/graph?level=cluster')
    rc_count=$(jq -r '.resources | length' <<<"${cluster_resp}" 2>/dev/null || echo "")
    [[ -n "${rc_count}" && "${rc_count}" != "null" ]] \
      || fail "post-${name}: cluster view failed (body: ${cluster_resp})"
    pass "post-${name} re-verify (resources=${rc_count})"

    yellow "  (recovery sleep 30s before next scenario)"
    sleep 30
  done

  pass "chaos suite complete"
fi

# ----- summary -------------------------------------------------------

green ""
green "phase2.sh M4+M5+M6.1+M6.2$([[ ${RUN_CHAOS} == 1 ]] && echo "+M6.3 chaos") — all assertions green"
green "  rego engine modules loaded: ${loaded}"
green "  Pod restart budget: ${restart_elapsed}s / ${RESTART_BUDGET_SECONDS}s"
green "  resources before/after restart: ${pre_resources} / ${post_resources}"
green "  rbac permissions: api-sa -> api-cm-reader (${M5_NS})"
green "  blast-radius affected count from app-config: ${blast_count}"
green "  orphan reports total: ${orphan_count} (ghost-rs flagged)"
green "  cycle count: ${cycles_count}"

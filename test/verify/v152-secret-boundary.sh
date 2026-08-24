#!/usr/bin/env bash
# Verifies the v1.5.1 -> v1.5.2 Secret data-boundary upgrade against a real
# Tier 2 database. Run "baseline" while the released v1.5.1 binary is ready,
# then run "upgraded" after the local v1.5.2 chart/image becomes ready.

set -euo pipefail

STAGE="${1:-}"
NS="${KUBEATLAS_NAMESPACE:-kubeatlas}"
RELEASE="${KUBEATLAS_RELEASE:-kubeatlas}"
PG_CLUSTER="${KUBEATLAS_PG_CLUSTER:-${RELEASE}-pg}"
PF_PORT="${KUBEATLAS_PF_PORT:-18082}"
CANARY_B64="a3ViZWF0bGFzLXYxNTItdXBncmFkZS1zZWNyZXQtY2FuYXJ5"
PF_PID=""

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS: %s\n' "$*"; }

for command_name in kubectl jq curl grep; do
  command -v "${command_name}" >/dev/null 2>&1 \
    || fail "missing required command: ${command_name}"
done

pg_pod="$({
  kubectl get pods --namespace "${NS}" \
    -l "cnpg.io/cluster=${PG_CLUSTER},cnpg.io/instanceRole=primary" \
    -o json
} | jq -r '.items[0].metadata.name // empty')"
[[ -n "${pg_pod}" ]] || fail "primary CNPG pod not found for ${PG_CLUSTER}"

sql_value() {
  kubectl exec --namespace "${NS}" "${pg_pod}" -c postgres -- \
    psql -U postgres -d kubeatlas -Atc "$1" | tr -d '[:space:]'
}

assert_sql() {
  local description="$1"
  local query="$2"
  local expected="$3"
  local actual
  actual="$(sql_value "${query}")"
  [[ "${actual}" == "${expected}" ]] \
    || fail "${description}: got ${actual:-<empty>}, want ${expected}"
  pass "${description} = ${expected}"
}

stop_port_forward() {
  if [[ -n "${PF_PID}" ]] && kill -0 "${PF_PID}" 2>/dev/null; then
    kill "${PF_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
  fi
  PF_PID=""
}

start_port_forward() {
  # Target the Deployment explicitly. Snapshot Jobs intentionally share the
  # app labels used by the Service selector but do not expose the HTTP port, so
  # service port-forward can race and select a snapshot Pod.
  kubectl port-forward --namespace "${NS}" "deployment/${RELEASE}" \
    "${PF_PORT}:8080" >/tmp/kubeatlas-v152-secret-boundary-pf.log 2>&1 &
  PF_PID=$!
  trap stop_port_forward EXIT
  for _ in $(seq 1 60); do
    if curl -fsS --max-time 1 \
      "http://127.0.0.1:${PF_PORT}/readyz" >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done
  fail "KubeAtlas API did not become reachable on ${PF_PORT}"
}

case "${STAGE}" in
  baseline)
    assert_sql "baseline schema" \
      "SELECT max(version) FROM public.schema_migrations" "10"

    # Initial informer delivery and async snapshot writes may lag readiness by
    # a few seconds. Require both unsafe copies to exist before claiming the
    # migration later removed them.
    deadline=$((SECONDS + 60))
    while (( SECONDS < deadline )); do
      resource_rows="$(sql_value "
        SELECT count(*) FROM public.resources
        WHERE id = '${NS}/Secret/database-credentials'
          AND data -> 'raw' -> 'data' ->> 'password' = '${CANARY_B64}'
      ")"
      event_rows="$(sql_value "
        SELECT count(*) FROM public.resource_events
        WHERE namespace = '${NS}' AND kind = 'Secret'
          AND data -> 'data' ->> 'password' = '${CANARY_B64}'
      ")"
      if [[ "${resource_rows}" == "1" && "${event_rows}" -ge 1 ]]; then
        pass "v1.5.1 stored the Secret canary in current state and history"
        exit 0
      fi
      sleep 2
    done
    fail "v1.5.1 did not persist both Secret canary copies"
    ;;

  upgraded)
    assert_sql "upgraded schema" \
      "SELECT max(version) FROM public.schema_migrations" "11"
    assert_sql "Secret canary rows" \
      "SELECT count(*) FROM public.resources WHERE data::text LIKE '%${CANARY_B64}%'" "0"
    assert_sql "event canary rows" \
      "SELECT count(*) FROM public.resource_events WHERE data::text LIKE '%${CANARY_B64}%'" "0"
    assert_sql "event payload rows" \
      "SELECT count(*) FROM public.resource_events WHERE data IS NOT NULL" "0"
    assert_sql "unsafe Secret rows" \
      "SELECT count(*) FROM public.resources
       WHERE (data ->> 'kind' = 'Secret' OR id ~ '(^|:)[^/]+/Secret/[^/]+$')
         AND (
           jsonb_typeof(data) = 'object'
           AND (data - ARRAY['kind','name','namespace','annotations','clusterId']::text[]) = '{}'::jsonb
           AND jsonb_typeof(data -> 'kind') = 'string'
           AND data ->> 'kind' = 'Secret'
           AND jsonb_typeof(data -> 'name') = 'string'
           AND data ->> 'name' <> ''
           AND jsonb_typeof(data -> 'namespace') = 'string'
           AND data ->> 'namespace' <> ''
           AND data -> 'annotations' = jsonb_build_object('kubeatlas.io/reference-only','true')
           AND (NOT (data ? 'clusterId') OR jsonb_typeof(data -> 'clusterId') = 'string')
           AND id = (
             CASE WHEN COALESCE(data ->> 'clusterId', '') = '' THEN ''
                  ELSE (data ->> 'clusterId') || ':' END
             || (data ->> 'namespace') || '/Secret/' || (data ->> 'name')
           )
         ) IS NOT TRUE" "0"
    assert_sql "outgoing Secret edges" \
      "SELECT count(*) FROM public.edges
       WHERE from_id = '${NS}/Secret/database-credentials'" "0"
    assert_sql "security constraints" \
      "SELECT count(*) FROM pg_constraint
       WHERE conname IN ('resources_secret_reference_only','resource_events_metadata_only')" "2"

    incoming_edges="$(sql_value "
      SELECT count(*) FROM public.edges
      WHERE to_id = '${NS}/Secret/database-credentials'
    ")"
    [[ "${incoming_edges}" -ge 1 ]] \
      || fail "initial resync did not restore incoming Secret references"
    pass "initial resync restored ${incoming_edges} incoming Secret references"

    secret_access="$(
      kubectl auth can-i list secrets --all-namespaces \
        --as="system:serviceaccount:${NS}:${RELEASE}" 2>/dev/null || true
    )"
    [[ "${secret_access}" == "no" ]] \
      || fail "runtime ServiceAccount can list Secrets: ${secret_access}"
    pass "runtime ServiceAccount cannot list Secrets"

    deployment_access="$(
      kubectl auth can-i list deployments.apps --all-namespaces \
        --as="system:serviceaccount:${NS}:${RELEASE}"
    )"
    [[ "${deployment_access}" == "yes" ]] \
      || fail "runtime ServiceAccount cannot list Deployments"

    start_port_forward
    secret_response="$(curl -fsS --max-time 10 \
      "http://127.0.0.1:${PF_PORT}/api/v1/resources/${NS}/Secret/database-credentials")"
    jq -e --arg ns "${NS}" '
      .resource == {
        kind: "Secret",
        name: "database-credentials",
        namespace: $ns,
        annotations: {"kubeatlas.io/reference-only": "true"}
      }
      and (.incoming | length >= 1)
      and (.outgoing | length == 0)
    ' <<<"${secret_response}" >/dev/null \
      || fail "Secret API resource is not a strict reference-only placeholder"
    if grep -Eq "kubeatlas-v152-upgrade-(secret|token)-canary|${CANARY_B64}" \
      <<<"${secret_response}"; then
      fail "Secret canary reached the API"
    fi
    pass "Tier 2 API exposes only the reference-only Secret placeholder"

    app_pod="$({
      kubectl get pods --namespace "${NS}" \
        -l "app.kubernetes.io/name=kubeatlas,app.kubernetes.io/instance=${RELEASE}" \
        -o json
    } | jq -r '
      [.items[] | select(.metadata.labels["pod-template-hash"] != null)]
      | sort_by(.metadata.creationTimestamp)
      | last
      | .metadata.name // empty
    ')"
    [[ -n "${app_pod}" ]] || fail "running KubeAtlas Deployment pod not found"
    if kubectl logs --namespace "${NS}" "${app_pod}" -c kubeatlas \
      | grep -Eq "kubeatlas-v152-upgrade-(secret|token)-canary|${CANARY_B64}"; then
      fail "Secret canary reached KubeAtlas logs"
    fi
    pass "Secret canary is absent from KubeAtlas logs"

    job_name="${RELEASE}-snapshot-v152-verify"
    kubectl delete job --namespace "${NS}" "${job_name}" \
      --ignore-not-found >/dev/null
    kubectl create job --namespace "${NS}" "${job_name}" \
      --from="cronjob/${RELEASE}-snapshot" >/dev/null
    kubectl wait --namespace "${NS}" --for=condition=complete \
      "job/${job_name}" --timeout=2m >/dev/null
    assert_sql "snapshot event payload rows after periodic trigger" \
      "SELECT count(*) FROM public.resource_events WHERE data IS NOT NULL" "0"
    pass "snapshot trigger crosses only its narrow NetworkPolicy path"
    ;;

  *)
    fail "usage: $0 baseline|upgraded"
    ;;
esac

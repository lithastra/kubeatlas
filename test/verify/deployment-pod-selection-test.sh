#!/usr/bin/env bash

# Synthetic regression only: no Kubernetes access, writes, or fault injection.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/deployment-pod.sh"

fixture=$(jq -n '
  def pod($name; $time): {
    metadata: {name:$name,creationTimestamp:$time,
      ownerReferences:[{kind:"ReplicaSet",controller:true,uid:"owned-rs"}]},
    spec: {containers:[{name:"kubeatlas"}]},
    status: {phase:"Running",containerStatuses:[{name:"kubeatlas",ready:true}],
      conditions:[{type:"Ready",status:"True"}]}
  };
  {items:[
    pod("old-app"; "2026-09-24T00:00:00Z"),
    pod("replacement-app"; "2026-09-24T00:01:00Z"),
    (pod("legacy-snapshot-job"; "2026-09-24T00:02:00Z")
      | .metadata.ownerReferences=[{kind:"Job",controller:true,uid:"job"}]),
    (pod("terminating-app"; "2026-09-24T00:03:00Z")
      | .metadata.deletionTimestamp="2026-09-24T00:04:00Z"),
    (pod("other-deployment"; "2026-09-24T00:04:00Z")
      | .metadata.ownerReferences[0].uid="other-rs"),
    (pod("unready-app"; "2026-09-24T00:05:00Z")
      | .status.conditions[0].status="False"),
    (pod("container-not-ready"; "2026-09-24T00:06:00Z")
      | .status.containerStatuses[0].ready=false),
    (pod("not-application"; "2026-09-24T00:07:00Z")
      | .spec.containers[0].name="snapshot-trigger"),
    (pod("completed-app"; "2026-09-24T00:08:00Z")
      | .status.phase="Succeeded"),
    (pod("not-controller"; "2026-09-24T00:09:00Z")
      | .metadata.ownerReferences[0].controller=false)
  ]}')

expect() {
  local expected=$1 excluded=$2 owners=$3 input=$4 actual
  actual=$(kubeatlas_select_ready_app_pod "${excluded}" "${owners}" <<<"${input}")
  [[ "${actual}" == "${expected}" ]] || {
    printf 'Pod selection: expected <%s>, got <%s>\n' "${expected}" "${actual}" >&2
    exit 1
  }
}

expect replacement-app '' '["owned-rs"]' "${fixture}"
expect replacement-app old-app '["owned-rs"]' "${fixture}"
expect old-app replacement-app '["owned-rs"]' "${fixture}"
expect '' old-app '["owned-rs"]' "$(jq '.items |= map(select(.metadata.name != "replacement-app"))' <<<"${fixture}")"
expect '' '' '[]' "${fixture}"
expect '' '' '["owned-rs"]' '{"items":[]}'
expect '' '' '["owned-rs"]' '{"items":[{"metadata":{"name":"no-status"}}]}'

# Exercise the live-query wrapper with a read-only in-process kubectl mock.
kubectl() {
  case "$*" in
    'get deployment -n custom-ns custom-release -o json')
      printf '%s\n' '{"metadata":{"uid":"deployment-uid"},"spec":{"selector":{"matchLabels":{"app":"custom-release"}}}}' ;;
    'get replicasets -n custom-ns -l app=custom-release -o json')
      printf '%s\n' '{"items":[{"metadata":{"uid":"owned-rs","ownerReferences":[{"kind":"Deployment","controller":true,"uid":"deployment-uid"}]}},{"metadata":{"uid":"other-rs","ownerReferences":[{"kind":"Deployment","controller":true,"uid":"other-deployment"}]}}]}' ;;
    'get pods -n custom-ns -l app=custom-release -o json') printf '%s\n' "${fixture}" ;;
    *) printf 'Unexpected kubectl invocation in mock\n' >&2; return 1 ;;
  esac
}
[[ $(kubeatlas_ready_deployment_pod custom-ns custom-release old-app) == replacement-app ]]
printf 'PASS: Deployment Pod selection excludes Jobs, wrong owners, terminating and unready Pods\n'

#!/usr/bin/env bash

# Select only Ready application Pods controlled by this Deployment's
# ReplicaSets. Historical snapshot Jobs may still carry the old broad app
# label, so neither labels nor Ready alone prove application ownership.
kubeatlas_select_ready_app_pod() {
  local excluded_pod=${1:-}
  local replica_set_uids=${2:?ReplicaSet UID array required}
  jq -r --arg excluded_pod "${excluded_pod}" --argjson owners "${replica_set_uids}" '
    [.items[]
      | select(.metadata.name != $excluded_pod)
      | select(.metadata.deletionTimestamp == null)
      | select(.status.phase == "Running")
      | select(any(.metadata.ownerReferences[]?;
          .kind == "ReplicaSet" and .controller == true
          and (.uid as $uid | $owners | index($uid) != null)))
      | select(any(.spec.containers[]?; .name == "kubeatlas"))
      | select(any(.status.containerStatuses[]?;
          .name == "kubeatlas" and .ready == true))
      | select(any(.status.conditions[]?;
          .type == "Ready" and .status == "True"))]
    | sort_by(.metadata.creationTimestamp, .metadata.name)
    | last
    | .metadata.name // empty'
}

kubeatlas_ready_deployment_pod() {
  local namespace=$1 deployment=$2 excluded_pod=${3:-}
  local deployment_json deployment_uid selector replica_sets owners
  deployment_json=$(kubectl get deployment -n "${namespace}" "${deployment}" -o json) || return 1
  deployment_uid=$(jq -er '.metadata.uid | select(type == "string" and length > 0)' <<<"${deployment_json}") || return 1
  selector=$(jq -er '.spec.selector.matchLabels | select(length > 0)
    | to_entries | sort_by(.key) | map(.key + "=" + .value) | join(",")' <<<"${deployment_json}") || return 1
  replica_sets=$(kubectl get replicasets -n "${namespace}" -l "${selector}" -o json) || return 1
  owners=$(jq -c --arg uid "${deployment_uid}" '[.items[]
    | select(.metadata.deletionTimestamp == null)
    | select(any(.metadata.ownerReferences[]?;
        .kind == "Deployment" and .controller == true and .uid == $uid))
    | .metadata.uid]' <<<"${replica_sets}") || return 1
  kubectl get pods -n "${namespace}" -l "${selector}" -o json \
    | kubeatlas_select_ready_app_pod "${excluded_pod}" "${owners}"
}

#!/usr/bin/env bash

v160_soak_event_json() {
  local name=$1
  local status=$2
  local recovery_seconds=$3
  local details=${4-}

  [[ -n "${details}" ]] || details='{}'
  jq -cn \
    --arg schema 'https://kubeatlas.lithastra.com/schemas/v160-soak-event-v1.json' \
    --arg name "${name}" --arg status "${status}" \
    --argjson captured_at_epoch "$(date +%s)" \
    --argjson recovery_seconds "${recovery_seconds}" \
    --argjson details "${details}" \
    '{"$schema": $schema, captured_at_epoch: $captured_at_epoch, name: $name, status: $status, recovery_seconds: $recovery_seconds, sentinel_absent: true, details: $details}'
}

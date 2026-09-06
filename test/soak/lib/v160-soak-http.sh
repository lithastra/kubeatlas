#!/usr/bin/env bash

# Fetch one bounded security surface while tolerating a transient transport
# failure. HTTP responses, including non-2xx responses, are returned to the
# caller so it can apply the endpoint-specific status contract. A persistent
# transport failure remains fatal to the soak.
v160_soak_http_get_with_retry() {
  local url=$1
  local timeout_seconds=$2
  local attempts=$3
  local retry_delay_seconds=$4
  local retry_log=$5
  local endpoint_label=$6
  local attempt response curl_status=0

  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if response=$(curl -sS --max-time "${timeout_seconds}" -w $'\n%{http_code}' \
      "${url}" 2>>"${retry_log}"); then
      printf '%s\n' "${response}"
      return 0
    else
      curl_status=$?
    fi

    printf 'transport_failure endpoint=%s attempt=%s/%s curl_exit=%s\n' \
      "${endpoint_label}" "${attempt}" "${attempts}" "${curl_status}" \
      >>"${retry_log}"
    if (( attempt < attempts && retry_delay_seconds > 0 )); then
      sleep "${retry_delay_seconds}"
    fi
  done

  return "${curl_status}"
}

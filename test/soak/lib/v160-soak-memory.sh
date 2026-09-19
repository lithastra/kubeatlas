#!/usr/bin/env bash

# Only emit allowlisted numeric observations. A missing/duplicate/invalid
# metric is an instrumentation failure, never a fabricated zero reading.
v160_soak_memory_json() {
  local metrics=$1 name value
  local names=(heap_alloc_bytes heap_live_bytes heap_goal_bytes heap_free_bytes
    heap_released_bytes heap_unused_bytes heap_stacks_bytes runtime_total_bytes gc_cycles_total)
  local args=()
  for name in "${names[@]}"; do
    value=$(awk -v key="kubeatlas_go_${name}" '$1 == key {print $2}' <<<"${metrics}")
    if [[ ! "${value}" =~ ^[0-9]+$ ]]; then
      printf 'soak memory observation missing or invalid: %s\n' "${name}" >&2
      return 1
    fi
    args+=(--argjson "${name}" "${value}")
  done
  jq -cn "${args[@]}" '$ARGS.named'
}

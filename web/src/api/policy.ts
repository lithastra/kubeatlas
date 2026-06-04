import { useQuery, type UseQueryResult } from '@tanstack/react-query';

import { ApiError, fetchJSON } from './client';
import type { ConstraintAffectedResponse, PolicyConstraint } from './types';

const apiBase = '/api/v1/policy';

// useConstraints wraps GET /api/v1/policy/constraints — every Gatekeeper
// Constraint and Kyverno (Cluster)Policy with its live violation count.
// The response is a bare array. An optional engine filters the list.
export function useConstraints(engine?: string): UseQueryResult<PolicyConstraint[], ApiError> {
  const qs = engine ? `?engine=${encodeURIComponent(engine)}` : '';
  return useQuery<PolicyConstraint[], ApiError>({
    queryKey: ['policy-constraints', engine ?? 'all'],
    queryFn: ({ signal }) => fetchJSON<PolicyConstraint[]>(`${apiBase}/constraints${qs}`, { signal }),
    refetchInterval: 60_000,
  });
}

// useConstraintAffected wraps
// GET /api/v1/policy/constraints/{name}/affected — the resources a named
// constraint enforces, each flagged with its violation status. Disabled
// until a constraint name is selected.
export function useConstraintAffected(
  name: string,
  enabled = true,
): UseQueryResult<ConstraintAffectedResponse, ApiError> {
  return useQuery<ConstraintAffectedResponse, ApiError>({
    queryKey: ['policy-affected', name],
    queryFn: ({ signal }) =>
      fetchJSON<ConstraintAffectedResponse>(
        `${apiBase}/constraints/${encodeURIComponent(name)}/affected`,
        { signal },
      ),
    enabled: enabled && Boolean(name),
  });
}

/* ============================================================
 * federation — hooks for the /api/v1/federation/* endpoints.
 *
 * `useFederationClusters` lists the cluster IDs known to the
 * federation manager. On non-federated installs the endpoint
 * returns `{ clusters: [] }` (200 OK) — the UI treats that as
 * single-cluster mode and renders the strip with one "local" item.
 * ============================================================ */
import { useQuery, type UseQueryResult } from '@tanstack/react-query';

import { ApiError, fetchJSON } from './client';

export interface FederationClustersResponse {
  clusters: string[];
}

const apiBase = '/api/v1';

export function useFederationClusters(): UseQueryResult<FederationClustersResponse, ApiError> {
  return useQuery<FederationClustersResponse, ApiError>({
    queryKey: ['federation-clusters'],
    queryFn: ({ signal }) =>
      fetchJSON<FederationClustersResponse>(`${apiBase}/federation/clusters`, { signal }),
    refetchInterval: 30_000,
    retry: (count, err) => {
      // 404 means the route isn't wired (older server) — don't retry.
      if (err instanceof ApiError && err.status === 404) return false;
      return count < 2;
    },
  });
}

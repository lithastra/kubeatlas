import { useQuery, type UseQueryResult } from '@tanstack/react-query';

import { useClusterSelection } from '../shell/ClusterSelectionContext';
import { ApiError, fetchJSON } from './client';
import { useFederationClusters, type FederationClustersResponse } from './federation';
import type { DiffResult, SnapshotListResponse } from './types';

export interface HistoryScope {
  // null is unresolved/blocked; empty string is explicitly standalone.
  clusterId: string | null;
  message: string | null;
}

export function resolveHistoryScope(data: FederationClustersResponse | undefined, selected: string | null, failed: boolean): HistoryScope {
  if (failed) return { clusterId: null, message: 'History scope is unavailable. Check cluster access and refresh the page.' };
  if (!data) return { clusterId: null, message: 'Checking history scope…' };
  if (data.mode === 'single' && !data.clusters?.length) return { clusterId: '', message: null };
  if (data.mode === 'federated' || data.clusters?.length) {
    if (selected && data.clusters?.includes(selected)) return { clusterId: selected, message: null };
    return { clusterId: null, message: 'Select one attached cluster in the cluster strip to read its history.' };
  }
  return { clusterId: null, message: 'History scope is unavailable. This server must report its single-cluster or federated mode.' };
}

export function useHistoryScope(): HistoryScope {
  const { selected } = useClusterSelection();
  const fleet = useFederationClusters();
  return resolveHistoryScope(fleet.data, selected, fleet.isError);
}

const apiBase = '/api/v1';

// All callers explicitly bind a scope. No unscoped fallback or automatic
// failure retry; cached data is never shown while a replacement read is active.
export function useSnapshots(cluster: string | null): UseQueryResult<SnapshotListResponse, ApiError> {
  const params = new URLSearchParams();
  if (cluster) params.set('cluster', cluster);
  const suffix = params.size ? `?${params.toString()}` : '';
  return useQuery<SnapshotListResponse, ApiError>({
    queryKey: ['snapshots', cluster],
    enabled: cluster !== null,
    queryFn: ({ signal }) => fetchJSON<SnapshotListResponse>(`${apiBase}/snapshots${suffix}`, { signal }),
    staleTime: 0,
    refetchInterval: (query) => query.state.error ? false : 60_000,
    refetchOnReconnect: false,
    retry: false,
  });
}

export function useSnapshotDiff(from: string, to: string, namespace: string, cluster: string | null): UseQueryResult<DiffResult, ApiError> {
  const params = new URLSearchParams();
  if (from) params.set('from', from);
  if (to) params.set('to', to);
  if (namespace) params.set('namespace', namespace);
  if (cluster) params.set('cluster', cluster);
  return useQuery<DiffResult, ApiError>({
    queryKey: ['snapshot-diff', cluster, from, to, namespace],
    enabled: from !== '' && cluster !== null,
    queryFn: ({ signal }) => fetchJSON<DiffResult>(`${apiBase}/snapshots/diff?${params.toString()}`, { signal }),
    staleTime: 0,
    refetchOnReconnect: false,
    retry: false,
  });
}

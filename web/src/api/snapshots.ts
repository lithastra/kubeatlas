import { useQuery, type UseQueryResult } from '@tanstack/react-query';

import { ApiError, fetchJSON } from './client';
import type { DiffResult, SnapshotListResponse } from './types';

const apiBase = '/api/v1';

// useSnapshots wraps GET /api/v1/snapshots — the list of periodic
// full-sync markers. Returns 503 on Tier 1 / snapshots-disabled
// installs; the UI surfaces that case as "snapshots not enabled"
// rather than a generic error toast.
export function useSnapshots(): UseQueryResult<SnapshotListResponse, ApiError> {
  return useQuery<SnapshotListResponse, ApiError>({
    queryKey: ['snapshots'],
    queryFn: ({ signal }) =>
      fetchJSON<SnapshotListResponse>(`${apiBase}/snapshots`, { signal }),
    // Snapshot markers tick on a chart-configured cron (default 5m);
    // re-polling every 60s keeps the timeline lively without thrashing.
    refetchInterval: 60_000,
    retry: (_, err) =>
      // A 503 means snapshots aren't enabled — don't retry, surface
      // the dedicated banner immediately.
      !(err instanceof ApiError) || err.status !== 503,
  });
}

// useSnapshotDiff wraps GET /api/v1/snapshots/diff. `from` and `to`
// accept the same wire format as the server: "now", a duration
// ("5m" / "1h" / "7d", read as "ago"), or an RFC3339 timestamp.
// `namespace` empty diffs the whole cluster. The query is disabled
// when `from` is empty so the picker can mount before a window has
// been chosen.
export function useSnapshotDiff(
  from: string,
  to: string,
  namespace: string,
): UseQueryResult<DiffResult, ApiError> {
  const params = new URLSearchParams();
  if (from) params.set('from', from);
  if (to) params.set('to', to);
  if (namespace) params.set('namespace', namespace);

  return useQuery<DiffResult, ApiError>({
    queryKey: ['snapshot-diff', from, to, namespace],
    enabled: from !== '',
    queryFn: ({ signal }) =>
      fetchJSON<DiffResult>(`${apiBase}/snapshots/diff?${params.toString()}`, { signal }),
    retry: (_, err) => !(err instanceof ApiError) || err.status >= 500,
  });
}

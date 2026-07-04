import { useQuery, type UseQueryResult } from '@tanstack/react-query';

import { ApiError, fetchJSON } from './client';
import type { OtelOverlayResponse } from './types';

// useOtelOverlay wraps GET /api/v1/otel/overlay — the observed
// CALLS_AT_RUNTIME edges the correlator inferred from OTLP traces, for
// one namespace. The overlay is Tier 2 + otel.enabled only; a Tier 1 /
// otel-off server answers 503, which surfaces as an ApiError the caller
// can render as "overlay not available". retry is off so a 503 doesn't
// hammer the endpoint, and the query only runs while `enabled`.
export function useOtelOverlay(
  namespace: string,
  enabled: boolean,
): UseQueryResult<OtelOverlayResponse, ApiError> {
  const qs = namespace ? `?namespace=${encodeURIComponent(namespace)}` : '';
  return useQuery<OtelOverlayResponse, ApiError>({
    queryKey: ['otel-overlay', namespace],
    queryFn: ({ signal }) =>
      fetchJSON<OtelOverlayResponse>(`/api/v1/otel/overlay${qs}`, { signal }),
    enabled,
    retry: false,
    refetchInterval: 30_000,
  });
}

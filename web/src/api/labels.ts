import { useQuery } from '@tanstack/react-query';

import { fetchJSON } from './client';
import type { LabelsResponse } from './types';

const apiBase = '/api/v1alpha1';

// useLabels wraps GET /labels (F-114) — every label key in the
// cluster with its most common values. It feeds the LabelFilter
// control's key / value pickers. The result is small and changes
// slowly, so the default React Query caching is left as-is.
export function useLabels() {
  return useQuery<LabelsResponse>({
    queryKey: ['labels'],
    queryFn: ({ signal }) => fetchJSON<LabelsResponse>(`${apiBase}/labels`, { signal }),
  });
}

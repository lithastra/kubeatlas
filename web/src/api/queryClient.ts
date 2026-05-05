import { QueryClient } from '@tanstack/react-query';

// Single QueryClient shared across the app via QueryClientProvider in
// main.tsx. Defaults match the spec: 30s staleTime so navigating
// between pages doesn't trigger a refetch storm; 5-minute gcTime so
// cached data survives a tab switch but doesn't leak forever.
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      gcTime: 5 * 60_000,
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
});

import type { WatchClient } from './websocket';

// The WatchClient singleton lives on window so dev hot reload doesn't
// drop the connection between module re-evaluations. main.tsx writes
// the reference; components read it via getWatchClient().
//
// Returns null in non-browser contexts (jest jsdom in tests that don't
// boot main.tsx) so callers can short-circuit safely.
export function getWatchClient(): WatchClient | null {
  if (typeof window === 'undefined') return null;
  const w = window as unknown as { __kubeatlasWatch?: WatchClient };
  return w.__kubeatlasWatch ?? null;
}

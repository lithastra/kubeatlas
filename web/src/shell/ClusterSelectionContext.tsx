/* ============================================================
 * ClusterSelectionContext — federation cluster picker state.
 *
 * Holds the currently-focused cluster ID (null = "all clusters",
 * the L0 federation view). LeftClusterStrip writes; downstream
 * code (graph queries, the right panel, the canvas) reads. For the
 * v1.3 ship the graph layer hasn't been swapped to the federation
 * endpoint yet — this context makes the picker functional so the
 * IA is in place; the graph refetch wiring lands alongside the
 * federation graph view.
 * ============================================================ */
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react';

interface ClusterSelectionState {
  selected: string | null;
  setSelected: (next: string | null) => void;
}

const ClusterSelectionCtx = createContext<ClusterSelectionState | null>(null);

export function useClusterSelection(): ClusterSelectionState {
  const ctx = useContext(ClusterSelectionCtx);
  if (!ctx) {
    throw new Error('useClusterSelection must be used inside <ClusterSelectionProvider>');
  }
  return ctx;
}

export function ClusterSelectionProvider({ children }: { children: ReactNode }) {
  const [selected, setSelectedState] = useState<string | null>(null);
  const setSelected = useCallback((next: string | null) => setSelectedState(next), []);
  const value = useMemo<ClusterSelectionState>(
    () => ({ selected, setSelected }),
    [selected, setSelected],
  );
  return (
    <ClusterSelectionCtx.Provider value={value}>{children}</ClusterSelectionCtx.Provider>
  );
}

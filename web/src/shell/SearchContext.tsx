/* ============================================================
 * SearchContext — palette open-state + matched node IDs.
 *
 * The ⌘K palette is a shell-level affordance (any view can summon
 * it) and its results decorate the underlying graph rather than
 * replacing it. Two slices live here:
 *
 *   - `open`: whether the palette overlay is mounted. AtlasShell
 *     subscribes a global ⌘K / Ctrl-K handler that flips this.
 *   - `matchedIds`: the set of resource IDs the most recent query
 *     returned. TopologyView reads this and toggles a `match` flag
 *     on the cytoscape nodes so matches stay visible on the canvas
 *     even after the overlay closes (the IA's search-folds-into-
 *     graph principle).
 * ============================================================ */
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react';

interface SearchState {
  open: boolean;
  setOpen: (next: boolean) => void;
  toggle: () => void;
  matchedIds: ReadonlySet<string>;
  setMatchedIds: (ids: Iterable<string>) => void;
  clearMatches: () => void;
}

const SearchCtx = createContext<SearchState | null>(null);

export function useSearchOverlay(): SearchState {
  const ctx = useContext(SearchCtx);
  if (!ctx) {
    throw new Error('useSearchOverlay must be used inside <SearchProvider>');
  }
  return ctx;
}

export function SearchProvider({ children }: { children: ReactNode }) {
  const [open, setOpenState] = useState(false);
  const [matchedIds, setMatchedIdsState] = useState<ReadonlySet<string>>(
    () => new Set(),
  );

  const setOpen = useCallback((next: boolean) => setOpenState(next), []);
  const toggle = useCallback(() => setOpenState((v) => !v), []);
  const setMatchedIds = useCallback(
    (ids: Iterable<string>) => setMatchedIdsState(new Set(ids)),
    [],
  );
  const clearMatches = useCallback(
    () => setMatchedIdsState(new Set()),
    [],
  );

  const value = useMemo<SearchState>(
    () => ({ open, setOpen, toggle, matchedIds, setMatchedIds, clearMatches }),
    [open, setOpen, toggle, matchedIds, setMatchedIds, clearMatches],
  );
  return <SearchCtx.Provider value={value}>{children}</SearchCtx.Provider>;
}

/* ============================================================
 * DiffModeContext — time-axis anchor + derived diff-mode flag.
 *
 * The cartography time axis is a single-dimensional comparison
 * primitive: NOW is always the playhead; the operator sets an
 * "anchor" somewhere in the past, and the canvas + right panel
 * switch into diff mode showing what's changed between the two.
 * This context holds the anchor as a duration string the snapshot
 * diff API accepts ("1h", "4h", "24h", "7d", or RFC3339); null
 * means no anchor → no diff mode.
 *
 * Esc clears the anchor (handled in AtlasShell). The drag-anchor
 * interaction is queued for a follow-up; this commit ships preset
 * windows + the diff overlay machinery.
 * ============================================================ */
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react';

interface DiffModeState {
  anchor: string | null;
  active: boolean;
  setAnchor: (next: string | null) => void;
  exit: () => void;
}

const DiffModeCtx = createContext<DiffModeState | null>(null);

export function useDiffMode(): DiffModeState {
  const ctx = useContext(DiffModeCtx);
  if (!ctx) {
    throw new Error('useDiffMode must be used inside <DiffModeProvider>');
  }
  return ctx;
}

export function DiffModeProvider({ children }: { children: ReactNode }) {
  const [anchor, setAnchorState] = useState<string | null>(null);
  const setAnchor = useCallback((next: string | null) => setAnchorState(next), []);
  const exit = useCallback(() => setAnchorState(null), []);
  const value = useMemo<DiffModeState>(
    () => ({ anchor, active: anchor != null, setAnchor, exit }),
    [anchor, setAnchor, exit],
  );
  return <DiffModeCtx.Provider value={value}>{children}</DiffModeCtx.Provider>;
}

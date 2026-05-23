/* ============================================================
 * RightPanelContext — anything-can-fill-the-right-panel slot.
 *
 * The cartography shell renders one persistent right panel. Views
 * push content into it by calling `setContent(node)`; the shell
 * reads `content` and renders it. Decouples shell layout from view
 * logic so the topology view, the federated graph view, the search
 * results view, and the snapshots view can all populate the same
 * panel without each having to know about layout primitives.
 *
 * setContent(null) closes the panel — used by the in-shell close
 * button and by views when their selection clears.
 * ============================================================ */
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react';

interface RightPanelState {
  content: ReactNode | null;
  setContent: (next: ReactNode | null) => void;
}

const RightPanelCtx = createContext<RightPanelState | null>(null);

export function useRightPanel(): RightPanelState {
  const ctx = useContext(RightPanelCtx);
  if (!ctx) {
    throw new Error('useRightPanel must be used inside <RightPanelProvider>');
  }
  return ctx;
}

export function RightPanelProvider({ children }: { children: ReactNode }) {
  const [content, setContentState] = useState<ReactNode | null>(null);
  const setContent = useCallback((next: ReactNode | null) => setContentState(next), []);
  const value = useMemo<RightPanelState>(
    () => ({ content, setContent }),
    [content, setContent],
  );
  return <RightPanelCtx.Provider value={value}>{children}</RightPanelCtx.Provider>;
}

/* ============================================================
 * BlastRadiusContext — graph-mode state for the blast-radius view.
 *
 * Blast radius isn't a route; it's a mode of the topology canvas.
 * The context here holds whether the mode is active, the root node
 * the traversal anchors on, the current depth limit, and the
 * direction (downstream / upstream / both). Components subscribe:
 *
 *   - TopologyView reads it to dim non-reachable nodes/edges.
 *   - TopologyPage swaps the right panel content for the blast
 *     radius summary while active.
 *   - AtlasShell renders the mode banner + depth/direction toolbar.
 * ============================================================ */
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react';

import type { BlastDirection } from '../lib/blastRadius';

interface BlastRadiusState {
  active: boolean;
  rootId: string | null;
  depth: number;
  direction: BlastDirection;
  enter: (rootId: string) => void;
  exit: () => void;
  setDepth: (next: number) => void;
  setDirection: (next: BlastDirection) => void;
}

const BlastRadiusCtx = createContext<BlastRadiusState | null>(null);

export function useBlastRadius(): BlastRadiusState {
  const ctx = useContext(BlastRadiusCtx);
  if (!ctx) {
    throw new Error('useBlastRadius must be used inside <BlastRadiusProvider>');
  }
  return ctx;
}

const DEFAULT_DEPTH = 3;
const DEFAULT_DIRECTION: BlastDirection = 'downstream';

export function BlastRadiusProvider({ children }: { children: ReactNode }) {
  const [active, setActive] = useState(false);
  const [rootId, setRootId] = useState<string | null>(null);
  const [depth, setDepthState] = useState(DEFAULT_DEPTH);
  const [direction, setDirectionState] = useState<BlastDirection>(DEFAULT_DIRECTION);

  const enter = useCallback((id: string) => {
    setRootId(id);
    setDepthState(DEFAULT_DEPTH);
    setDirectionState(DEFAULT_DIRECTION);
    setActive(true);
  }, []);

  const exit = useCallback(() => {
    setActive(false);
    setRootId(null);
  }, []);

  const setDepth = useCallback((n: number) => setDepthState(n), []);
  const setDirection = useCallback((d: BlastDirection) => setDirectionState(d), []);

  const value = useMemo<BlastRadiusState>(
    () => ({ active, rootId, depth, direction, enter, exit, setDepth, setDirection }),
    [active, rootId, depth, direction, enter, exit, setDepth, setDirection],
  );

  return <BlastRadiusCtx.Provider value={value}>{children}</BlastRadiusCtx.Provider>;
}

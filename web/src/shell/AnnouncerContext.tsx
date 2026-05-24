/* ============================================================
 * AnnouncerContext — single polite live-region for the whole shell.
 *
 * Screen-reader users can't see the canvas decoration that mode
 * changes apply (blast-radius dim, diff highlights, cluster
 * focus). Mode controllers call `announce("blast radius on
 * payments/api · 12 affected")` and the live region under
 * AtlasShell speaks the message at the SR cadence.
 *
 * Use `polite` (not `assertive`) so announcements don't interrupt
 * what the operator is currently reading. The region is hidden
 * visually (CSS visually-hidden) but stays in the accessibility
 * tree.
 * ============================================================ */
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';

interface AnnouncerState {
  message: string;
  announce: (next: string) => void;
}

const AnnouncerCtx = createContext<AnnouncerState | null>(null);

export function useAnnouncer(): AnnouncerState {
  const ctx = useContext(AnnouncerCtx);
  if (!ctx) {
    throw new Error('useAnnouncer must be used inside <AnnouncerProvider>');
  }
  return ctx;
}

export function AnnouncerProvider({ children }: { children: ReactNode }) {
  const [message, setMessage] = useState('');
  // Track the last announcement so repeated identical messages
  // still re-fire (some screen readers ignore an unchanged
  // textContent). We prepend a zero-width space and toggle it to
  // force a DOM diff without altering the visible / spoken text.
  const togglerRef = useRef(false);

  const announce = useCallback((next: string) => {
    togglerRef.current = !togglerRef.current;
    const prefix = togglerRef.current ? '​' : '';
    setMessage(`${prefix}${next}`);
  }, []);

  const value = useMemo<AnnouncerState>(
    () => ({ message, announce }),
    [message, announce],
  );
  return <AnnouncerCtx.Provider value={value}>{children}</AnnouncerCtx.Provider>;
}

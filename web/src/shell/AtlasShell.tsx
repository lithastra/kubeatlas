/* ============================================================
 * AtlasShell — cartography chrome around the canvas.
 *
 *   +-----------------------------------------------+
 *   | TopBar (48px)                                 |
 *   +-----------------------------------------------+
 *   | TimeAxisBar (32px)                            |
 *   +----+--------------------------------+---------+
 *   |    |                                |         |
 *   | L  |   canvas (GridBackground)      | right   |
 *   | C  |                                | panel   |
 *   | S  |                                | (400px) |
 *   |    |                       Compass  |         |
 *   +----+--------------------------------+---------+
 *
 * Standalone shell only. The Headlamp plugin variant (embedded=true)
 * skips top bar + time axis + left strip and hands the body to the
 * host. The current commit ships the standalone path; the embedded
 * branch needs Headlamp's host context, which lives in the future
 * lithastra/kubeatlas-headlamp-plugin integration.
 * ============================================================ */
import { Box } from '@mui/material';
import { useEffect, useState, type ReactNode } from 'react';

import { Panel } from '../design';
import { BlastRadiusBanner } from './BlastRadiusBanner';
import { BlastRadiusControls } from './BlastRadiusControls';
import { useBlastRadius } from './BlastRadiusContext';
import { DiffModeBanner } from './DiffModeBanner';
import { useDiffMode } from './DiffModeContext';
import { CommandPalette } from './CommandPalette';
import { CompassWidget } from './CompassWidget';
import { GridBackground } from './GridBackground';
import { LeftClusterStrip } from './LeftClusterStrip';
import { useRightPanel } from './RightPanelContext';
import { useSearchOverlay } from './SearchContext';
import { TimeAxisBar } from './TimeAxisBar';
import { TopBar } from './TopBar';

interface AtlasShellProps {
  /** Hide all standalone chrome (top bar, time axis, left strip,
   *  right panel default-open) — for the Headlamp plugin embed. */
  embedded?: boolean;
  /** Right context panel content. Empty by default; views populate
   *  it via the M5 panel slot. */
  contextPanel?: ReactNode;
  children?: ReactNode;
}

export function AtlasShell({ embedded = false, contextPanel, children }: AtlasShellProps) {
  // The panel slot can come from a prop (legacy) or from any
  // descendant view via useRightPanel().setContent(...). Prop takes
  // priority so callers that want full control keep it.
  const ctx = useRightPanel();
  const search = useSearchOverlay();
  const blast = useBlastRadius();
  const diff = useDiffMode();
  const liveContent = contextPanel ?? ctx.content;
  const [panelOpen, setPanelOpen] = useState(liveContent != null);
  if (liveContent != null && !panelOpen) setPanelOpen(true);

  // Global ⌘K / Ctrl-K handler. Lives at the shell so any view (and
  // the Headlamp embed once the embedded branch lands) can summon
  // the palette without wiring its own keymap. Skip while focus is
  // inside a text-input so typing K in a search box doesn't fight.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
        const t = e.target as HTMLElement | null;
        const tag = t?.tagName ?? '';
        const inEditable =
          (tag === 'INPUT' || tag === 'TEXTAREA' || t?.isContentEditable) &&
          !search.open;
        if (inEditable) return;
        e.preventDefault();
        search.toggle();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [search]);

  // Global Esc handler: closes whichever explorer mode is active.
  // Blast radius is preferred when both are on (a rare composite);
  // diff anchor clears on Esc too. Stays out of the way otherwise
  // so MUI dialogs / menus keep owning Esc for their own dismisses.
  useEffect(() => {
    if (!blast.active && !diff.active) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      e.preventDefault();
      if (blast.active) blast.exit();
      else diff.exit();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [blast, diff]);
  const closePanel = () => {
    setPanelOpen(false);
    if (contextPanel == null) ctx.setContent(null);
  };
  return (
    <Box
      sx={{
        display: 'flex',
        flexDirection: 'column',
        height: '100vh',
        backgroundColor: 'var(--atlas-bg)',
        color: 'var(--atlas-text-1)',
      }}
    >
      {!embedded && <TopBar />}
      {!embedded && <TimeAxisBar />}
      <Box
        id="atlas-main"
        component="main"
        sx={{ display: 'flex', flexGrow: 1, minHeight: 0 }}
      >
        {!embedded && <LeftClusterStrip />}
        <GridBackground>
          {children}
          <CompassWidget />
          <BlastRadiusBanner />
          <BlastRadiusControls />
          <DiffModeBanner />
        </GridBackground>
        {panelOpen && liveContent != null && (
          <Panel
            variant="panel"
            padding={0}
            ariaLabel="Detail panel"
            sx={{
              width: 'var(--atlas-chrome-right-panel-width)',
              minWidth: 'var(--atlas-chrome-right-panel-min)',
              maxWidth: 'var(--atlas-chrome-right-panel-max)',
              overflow: 'auto',
            }}
          >
            <Box
              component="button"
              type="button"
              onClick={closePanel}
              sx={{
                width: '100%',
                textAlign: 'right',
                padding: 'var(--atlas-space-2) var(--atlas-space-3)',
                background: 'transparent',
                border: 'none',
                borderBottom: '1px solid var(--atlas-border)',
                cursor: 'pointer',
                fontFamily: 'var(--atlas-font-ui)',
                fontSize: 'var(--atlas-text-caption-size)',
                color: 'var(--atlas-text-2)',
              }}
              aria-label="Close detail panel"
            >
              close ✕
            </Box>
            <Box
              aria-live="polite"
              sx={{ padding: 'var(--atlas-space-4)' }}
            >
              {liveContent}
            </Box>
          </Panel>
        )}
      </Box>
      <CommandPalette />
    </Box>
  );
}

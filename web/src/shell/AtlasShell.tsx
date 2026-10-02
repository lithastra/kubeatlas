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
 * Below the medium breakpoint, detail overlays the canvas (not the
 * cluster strip). Its analysis controls stay inside the scrolling panel.
 *
 * Standalone shell only. The Headlamp plugin variant (embedded=true)
 * skips top bar + time axis + left strip and hands the body to the
 * host. The current commit ships the standalone path; the embedded
 * branch needs Headlamp's host context, which lives in the future
 * lithastra/kubeatlas-headlamp-plugin integration.
 * ============================================================ */
import { Box, useMediaQuery, useTheme } from '@mui/material';
import { useEffect, useRef, useState, type ReactNode } from 'react';

import { Panel } from '../design';
import { useAnnouncer } from './AnnouncerContext';
import { BlastRadiusBanner } from './BlastRadiusBanner';
import { BlastRadiusControls } from './BlastRadiusControls';
import { useBlastRadius } from './BlastRadiusContext';
import { useClusterSelection } from './ClusterSelectionContext';
import { DiffModeBanner } from './DiffModeBanner';
import { useDiffMode } from './DiffModeContext';
import { CommandPalette } from './CommandPalette';
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
  const cluster = useClusterSelection();
  const { message: announceMessage, announce } = useAnnouncer();

  // Screen-reader announcements on mode change. Each effect speaks
  // a short sentence the polite live region (rendered at the foot
  // of this component) will pick up. Skip the initial render so we
  // don't announce "no mode" / "all clusters" on first paint.
  useEffect(() => {
    if (blast.active && blast.rootId) {
      announce(`Impact analysis on ${blast.rootId}, depth ${blast.depth}, ${blast.direction}.`);
    }
  }, [blast.active, blast.rootId, blast.depth, blast.direction, announce]);

  useEffect(() => {
    if (diff.active && diff.anchor) {
      announce(`Diff mode against ${diff.anchor} ago.`);
    }
  }, [diff.active, diff.anchor, announce]);

  useEffect(() => {
    if (cluster.selected) {
      announce(`Focused cluster ${cluster.selected}.`);
    }
  }, [cluster.selected, announce]);

  useEffect(() => {
    if (search.open) announce('Command palette open.');
  }, [search.open, announce]);
  const liveContent = contextPanel ?? ctx.content;
  const [panelOpen, setPanelOpen] = useState(liveContent != null);
  if (liveContent != null && !panelOpen) setPanelOpen(true);
  const narrow = useMediaQuery(useTheme().breakpoints.down('md'));
  const hasPanel = panelOpen && liveContent != null;
  const overlayPanel = narrow && hasPanel;
  const mainRef = useRef<HTMLElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  // The covered canvas must not receive keyboard input. Move focus into the
  // panel once when it overlays the canvas, not on every response refresh.
  useEffect(() => {
    if (!overlayPanel) return;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const main = mainRef.current;
    return () => {
      if (previous?.isConnected && !previous.closest('[inert]')) previous.focus();
      else main?.focus();
    };
  }, [overlayPanel]);
  useEffect(() => {
    if (overlayPanel) closeRef.current?.focus();
  }, [overlayPanel, blast.active]);

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
        '@supports (height: 100dvh)': { height: '100dvh' },
        minWidth: 0,
        backgroundColor: 'var(--atlas-bg)',
        color: 'var(--atlas-text-1)',
      }}
    >
      {!embedded && <TopBar />}
      {!embedded && <TimeAxisBar />}
      <Box
        id="atlas-main"
        component="main"
        ref={mainRef}
        tabIndex={-1}
        sx={{ display: 'flex', flexGrow: 1, minHeight: 0, minWidth: 0, position: 'relative', overflow: 'hidden' }}
      >
        {!embedded && <LeftClusterStrip />}
        <GridBackground inert={overlayPanel} sx={{ '& > *': { maxHeight: '100%' } }}>
          {children}
          <DiffModeBanner />
        </GridBackground>
        {hasPanel && (
          <Panel
            variant="panel"
            padding={0}
            ariaLabel="Detail panel"
            sx={{
              position: { xs: 'absolute', md: 'relative' },
              top: 0,
              bottom: 0,
              right: 0,
              zIndex: { xs: 10, md: 'auto' },
              width: { xs: embedded ? '100%' : 'calc(100% - var(--atlas-chrome-left-cluster-strip))', md: 'var(--atlas-chrome-right-panel-width)' },
              minWidth: { xs: 0, md: 'var(--atlas-chrome-right-panel-min)' },
              maxWidth: { xs: 'none', md: 'var(--atlas-chrome-right-panel-max)' },
              flexShrink: 0,
              overflow: 'auto',
            }}
          >
            <Box
              component="button"
              ref={closeRef}
              type="button"
              onClick={blast.active ? blast.exit : closePanel}
              sx={{
                width: '100%',
                minHeight: 40,
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
              aria-label={blast.active ? 'Exit impact analysis' : 'Close detail panel'}
            >
              {blast.active ? 'Back to resource' : 'close ✕'}
            </Box>
            <BlastRadiusBanner />
            <BlastRadiusControls />
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
      {/* Polite live region for mode-change announcements. Visually
          hidden via the same clip-path/position trick the WAI-ARIA
          authoring practices recommend; stays in the accessibility
          tree so screen readers speak each new message. */}
      <Box
        role="status"
        aria-live="polite"
        aria-atomic="true"
        sx={{
          position: 'absolute',
          width: 1,
          height: 1,
          padding: 0,
          margin: -1,
          overflow: 'hidden',
          clip: 'rect(0 0 0 0)',
          whiteSpace: 'nowrap',
          border: 0,
        }}
      >
        {announceMessage}
      </Box>
    </Box>
  );
}

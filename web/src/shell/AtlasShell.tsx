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
import { useState, type ReactNode } from 'react';

import { Panel } from '../design';
import { CompassWidget } from './CompassWidget';
import { GridBackground } from './GridBackground';
import { LeftClusterStrip } from './LeftClusterStrip';
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
  const [panelOpen, setPanelOpen] = useState(contextPanel != null);
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
      <Box sx={{ display: 'flex', flexGrow: 1, minHeight: 0 }}>
        {!embedded && <LeftClusterStrip />}
        <GridBackground>
          {children}
          <CompassWidget />
        </GridBackground>
        {(contextPanel || panelOpen) && (
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
              onClick={() => setPanelOpen(false)}
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
            <Box sx={{ padding: 'var(--atlas-space-4)' }}>{contextPanel}</Box>
          </Panel>
        )}
      </Box>
    </Box>
  );
}

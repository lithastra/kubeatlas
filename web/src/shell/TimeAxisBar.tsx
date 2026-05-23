/* ============================================================
 * TimeAxisBar — 32px persistent time scrubber + anchor presets.
 *
 * The cartography time axis is always visible (it's the 4th
 * dimension of the explorer). The playhead lives at the right edge
 * = NOW. Operators set an anchor in the past via the preset chips;
 * the anchor switches the canvas + right panel into diff mode via
 * DiffModeContext.
 *
 * The drag-anchor / shift-click interaction from the design is
 * queued for a later pass; the preset chips cover the common
 * windows (1h / 4h / 24h / 7d) and are reachable by keyboard.
 * ============================================================ */
import { Box, Stack, Typography } from '@mui/material';

import { useDiffMode } from './DiffModeContext';

const ANCHOR_PRESETS = [
  { value: '1h', label: '1h ago' },
  { value: '4h', label: '4h ago' },
  { value: '24h', label: '24h ago' },
  { value: '7d', label: '7d ago' },
] as const;

export function TimeAxisBar() {
  const { anchor, setAnchor, exit } = useDiffMode();
  return (
    <Box
      role="region"
      aria-label="Time axis"
      sx={{
        height: 'var(--atlas-chrome-time-axis)',
        flexShrink: 0,
        backgroundColor: 'var(--atlas-bg)',
        borderBottom: '1px solid var(--atlas-border)',
        display: 'flex',
        alignItems: 'center',
        paddingInline: 'var(--atlas-space-4)',
        gap: 'var(--atlas-space-3)',
      }}
    >
      <Typography
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 'var(--atlas-text-caption-size)',
          color: 'var(--atlas-text-3)',
          flexShrink: 0,
        }}
      >
        {anchor ? `anchor: ${anchor} ago` : 'anchor:'}
      </Typography>

      <Stack direction="row" spacing={0.5} sx={{ flexShrink: 0 }}>
        {ANCHOR_PRESETS.map((p) => {
          const isActive = anchor === p.value;
          return (
            <Box
              key={p.value}
              component="button"
              type="button"
              onClick={() => setAnchor(isActive ? null : p.value)}
              aria-pressed={isActive}
              sx={{
                padding: '2px 8px',
                border: '1px solid',
                borderColor: isActive ? 'var(--atlas-select)' : 'var(--atlas-border)',
                background: isActive
                  ? 'color-mix(in srgb, var(--atlas-select) 18%, transparent)'
                  : 'transparent',
                fontFamily: 'var(--atlas-font-mono)',
                fontSize: 11,
                color: isActive ? 'var(--atlas-select)' : 'var(--atlas-text-2)',
                cursor: 'pointer',
                '&:hover': { borderColor: 'var(--atlas-select)' },
                '&:focus-visible': {
                  outline: '2px solid var(--atlas-select)',
                  outlineOffset: 1,
                },
              }}
            >
              {p.label}
            </Box>
          );
        })}
        {anchor && (
          <Box
            component="button"
            type="button"
            onClick={exit}
            sx={{
              padding: '2px 8px',
              border: '1px solid var(--atlas-border)',
              background: 'transparent',
              fontFamily: 'var(--atlas-font-mono)',
              fontSize: 11,
              color: 'var(--atlas-text-3)',
              cursor: 'pointer',
              ml: 1,
              '&:hover': { color: 'var(--atlas-text-1)' },
            }}
          >
            clear
          </Box>
        )}
      </Stack>

      <Box
        aria-hidden
        sx={{
          flexGrow: 1,
          height: 2,
          position: 'relative',
          background: 'var(--atlas-border)',
          // Optional anchor marker on the rail. Position is a rough
          // preset mapping; proper time-indexed positioning lands
          // with the drag-anchor follow-up.
          ...(anchor
            ? {
                '&::before': {
                  content: '""',
                  position: 'absolute',
                  top: -4,
                  left: anchorRailX(anchor),
                  width: 2,
                  height: 10,
                  background: '#7E6BA8',
                },
              }
            : {}),
          '&::after': {
            content: '""',
            position: 'absolute',
            top: -4,
            left: '100%',
            transform: 'translateX(-100%)',
            width: 2,
            height: 10,
            background: 'var(--atlas-select)',
          },
        }}
      />
      <Typography
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 'var(--atlas-text-caption-size)',
          color: 'var(--atlas-text-3)',
          flexShrink: 0,
        }}
      >
        now
      </Typography>
    </Box>
  );
}

function anchorRailX(anchor: string): string {
  switch (anchor) {
    case '1h':
      return '92%';
    case '4h':
      return '70%';
    case '24h':
      return '30%';
    case '7d':
      return '6%';
    default:
      return '50%';
  }
}

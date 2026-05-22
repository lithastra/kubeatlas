/* ============================================================
 * TimeAxisBar — 32px persistent time scrubber.
 *
 * Skeleton for the M5 time-axis behaviour: a playhead positioned
 * on a horizontal time scale with the diff anchor reachable via
 * shift-click. This commit ships only the visual rail; the scrub
 * interaction wires up alongside the diff view.
 * ============================================================ */
import { Box, Typography } from '@mui/material';

export function TimeAxisBar() {
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
        }}
      >
        now
      </Typography>
      <Box
        aria-hidden
        sx={{
          flexGrow: 1,
          height: 2,
          position: 'relative',
          background: 'var(--atlas-border)',
          // Playhead — centered for now; the M5 scrubber moves it.
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
        }}
      >
        time scrub in M5
      </Typography>
    </Box>
  );
}

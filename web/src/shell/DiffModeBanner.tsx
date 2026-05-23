/* ============================================================
 * DiffModeBanner — top-center diff-mode chip.
 *
 * Mirrors BlastRadiusBanner but coloured purple per the design's
 * diff-mode banner. Visible only while DiffModeContext.active.
 * ============================================================ */
import { Box, Typography } from '@mui/material';

import { useDiffMode } from './DiffModeContext';

interface DiffModeBannerProps {
  changeCount?: number;
}

export function DiffModeBanner({ changeCount }: DiffModeBannerProps) {
  const { active, anchor } = useDiffMode();
  if (!active || !anchor) return null;
  return (
    <Box
      role="status"
      aria-live="polite"
      sx={{
        position: 'absolute',
        top: 'var(--atlas-space-3)',
        left: '50%',
        transform: 'translateX(-50%)',
        zIndex: 6,
        backgroundColor: '#7E6BA8',
        color: 'var(--atlas-bg)',
        padding: '6px 14px',
        display: 'flex',
        alignItems: 'center',
        gap: 1.5,
        boxShadow: '0 2px 6px rgba(0,0,0,0.18)',
      }}
    >
      <Box
        sx={{
          width: 8,
          height: 8,
          borderRadius: '50%',
          backgroundColor: 'var(--atlas-bg)',
        }}
      />
      <Typography
        component="span"
        sx={{ fontFamily: 'var(--atlas-font-ui)', fontSize: 12, fontWeight: 600 }}
      >
        Diff mode · {anchor} ago vs Now
        {changeCount != null ? ` · ${changeCount} changes` : ''}
      </Typography>
      <Typography
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 11,
          opacity: 0.85,
          ml: 1.5,
        }}
      >
        Esc to exit
      </Typography>
    </Box>
  );
}

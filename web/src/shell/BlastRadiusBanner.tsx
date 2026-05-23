/* ============================================================
 * BlastRadiusBanner — top-center mode banner.
 *
 * Replaces nothing (the design's mockup shows it replacing the
 * search box; that's a future top-bar consolidation). For v1 it
 * floats over the canvas as a thin chip that calls out the active
 * mode, root, direction, depth, and a hint to exit. Rendering is
 * gated on BlastRadiusContext.active.
 * ============================================================ */
import { Box, Typography } from '@mui/material';

import { useBlastRadius } from './BlastRadiusContext';

const DIRECTION_LABEL = {
  downstream: 'downstream ↓',
  upstream: 'upstream ↑',
  both: 'both ↕',
} as const;

interface BlastRadiusBannerProps {
  affectedCount?: number;
}

export function BlastRadiusBanner({ affectedCount }: BlastRadiusBannerProps) {
  const { active, rootId, depth, direction } = useBlastRadius();
  if (!active || !rootId) return null;
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
        backgroundColor: 'var(--atlas-select)',
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
        Blast Radius · {DIRECTION_LABEL[direction]} · {depth === Infinity ? '∞' : depth} hops
        {affectedCount != null ? ` · ${affectedCount} resources affected` : ''}
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

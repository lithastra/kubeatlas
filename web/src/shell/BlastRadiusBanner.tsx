/* ============================================================
 * BlastRadiusBanner — in-panel analysis mode banner.
 *
 * Lives with the result instead of covering graph filters on small
 * canvases. Calls out the active relation and depth. Rendering is
 * gated on BlastRadiusContext.active.
 * ============================================================ */
import { Box, Typography } from '@mui/material';

import { useBlastRadius } from './BlastRadiusContext';

const DIRECTION_LABEL = {
  dependents: 'dependents',
  dependencies: 'dependencies',
} as const;

export function BlastRadiusBanner() {
  const { active, rootId, depth, direction } = useBlastRadius();
  if (!active || !rootId) return null;
  return (
    <Box
      role="status"
      aria-live="polite"
      sx={{
        backgroundColor: 'var(--atlas-select)',
        color: 'var(--atlas-bg)',
        padding: '6px 14px',
        display: 'flex',
        flexWrap: 'wrap',
        alignItems: 'center',
        gap: 1.5,
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
        Impact · {DIRECTION_LABEL[direction]} · {depth} hops
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

/* ============================================================
 * GridBackground — cartography latitude/longitude grid.
 *
 * The visual signature: an extremely soft 80px major + 20px minor
 * grid drawn as a CSS background on the canvas container. Stays
 * out of the Cytoscape render path so it costs nothing on pan/
 * zoom. The token values come from atlas-themes.css.
 *
 * Inserted as a child wrapper around the actual canvas; the
 * canvas's transparent background lets the grid show through.
 * ============================================================ */
import { Box, type SxProps, type Theme } from '@mui/material';
import { type ReactNode } from 'react';

interface GridBackgroundProps {
  children?: ReactNode;
  sx?: SxProps<Theme>;
}

export function GridBackground({ children, sx }: GridBackgroundProps) {
  return (
    <Box
      sx={[
        {
          position: 'relative',
          flexGrow: 1,
          minHeight: 0,
          backgroundColor: 'var(--atlas-bg)',
          // Two CSS gradients layered for the major / minor grid.
          // The intersections add to ~30% / 12% opacity at the
          // crossings; cells look like graph paper rather than dots.
          backgroundImage: `
            linear-gradient(to right,  color-mix(in srgb, var(--atlas-border) 30%, transparent) 1px, transparent 1px),
            linear-gradient(to bottom, color-mix(in srgb, var(--atlas-border) 30%, transparent) 1px, transparent 1px),
            linear-gradient(to right,  color-mix(in srgb, var(--atlas-border) 12%, transparent) 1px, transparent 1px),
            linear-gradient(to bottom, color-mix(in srgb, var(--atlas-border) 12%, transparent) 1px, transparent 1px)
          `,
          backgroundSize: `
            var(--atlas-grid-size) var(--atlas-grid-size),
            var(--atlas-grid-size) var(--atlas-grid-size),
            var(--atlas-grid-sub-size) var(--atlas-grid-sub-size),
            var(--atlas-grid-sub-size) var(--atlas-grid-sub-size)
          `,
        },
        ...(Array.isArray(sx) ? sx : sx ? [sx] : []),
      ]}
    >
      {children}
    </Box>
  );
}

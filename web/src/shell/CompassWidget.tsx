/* ============================================================
 * CompassWidget — bottom-right cartography signature.
 *
 * A small SVG rose (north arrow + cardinal marks) plus a numeric
 * scale chip. Positioned absolutely inside the canvas area so it
 * sits over the graph without participating in layout.
 *
 * Drawn inline (not via the icon sprite) so it can scale freely
 * and the cardinal labels stay legible at the design's 64px size.
 * ============================================================ */
import { Box, Typography } from '@mui/material';

interface CompassWidgetProps {
  /** Optional viewport-zoom-to-label. M5 wires this from cytoscape's
   *  current zoom; for now it shows a static scale label. */
  scaleLabel?: string;
}

export function CompassWidget({ scaleLabel = '50,000 ft' }: CompassWidgetProps) {
  return (
    <Box
      role="img"
      aria-label={`Compass · scale ${scaleLabel}`}
      sx={{
        position: 'absolute',
        right: 'var(--atlas-space-4)',
        bottom: 'var(--atlas-space-4)',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'flex-end',
        gap: 'var(--atlas-space-1)',
        pointerEvents: 'none',
        color: 'var(--atlas-text-2)',
      }}
    >
      <Box
        component="svg"
        viewBox="0 0 64 64"
        sx={{ width: 64, height: 64 }}
        aria-hidden
      >
        <circle cx="32" cy="32" r="28" fill="none" stroke="currentColor" strokeOpacity="0.4" />
        <circle cx="32" cy="32" r="20" fill="none" stroke="currentColor" strokeOpacity="0.2" />
        {/* Cardinal ticks */}
        <line x1="32" y1="4" x2="32" y2="12" stroke="currentColor" strokeOpacity="0.6" />
        <line x1="32" y1="52" x2="32" y2="60" stroke="currentColor" strokeOpacity="0.35" />
        <line x1="4" y1="32" x2="12" y2="32" stroke="currentColor" strokeOpacity="0.35" />
        <line x1="52" y1="32" x2="60" y2="32" stroke="currentColor" strokeOpacity="0.35" />
        {/* North arrow */}
        <path
          d="M32 12 L37 32 L32 28 L27 32 Z"
          fill="currentColor"
          fillOpacity="0.85"
        />
        <text
          x="32"
          y="22"
          textAnchor="middle"
          fontFamily="var(--atlas-font-mono)"
          fontSize="8"
          fill="var(--atlas-bg)"
        >
          N
        </text>
      </Box>
      <Typography
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 'var(--atlas-text-scale-figure-size)',
          color: 'var(--atlas-text-3)',
          backgroundColor: 'var(--atlas-bg)',
          borderRadius: 'var(--atlas-radius-1)',
          padding: '2px 6px',
          border: '1px solid var(--atlas-border)',
        }}
      >
        {scaleLabel}
      </Typography>
    </Box>
  );
}

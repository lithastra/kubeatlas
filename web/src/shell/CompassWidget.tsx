/* ============================================================
 * CompassWidget — cartography signature rose.
 *
 * A small SVG north arrow positioned top-right of the canvas. The
 * scale chip was removed once ZoomScaleWidget shipped (bottom-right,
 * shows the actual cytoscape zoom × L-band) — keeping the static
 * "50,000 ft" placeholder alongside a live scale was misleading.
 *
 * Drawn inline (not via the icon sprite) so it can scale freely
 * and the N glyph stays legible at the design's 56px size.
 * ============================================================ */
import { Box } from '@mui/material';

export function CompassWidget() {
  return (
    <Box
      role="img"
      aria-label="Compass · north up"
      sx={{
        position: 'absolute',
        right: 'var(--atlas-space-4)',
        top: 'var(--atlas-space-4)',
        pointerEvents: 'none',
        color: 'var(--atlas-text-2)',
        opacity: 0.7,
      }}
    >
      <Box
        component="svg"
        viewBox="0 0 64 64"
        sx={{ width: 56, height: 56 }}
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
    </Box>
  );
}

/* ============================================================
 * CompassWidget — cartography signature rose.
 *
 * A small SVG north arrow positioned bottom-left of the topology
 * canvas — bottom-right is owned by ZoomScaleWidget and top-right
 * by the chrome (theme switcher, etc.). The widget is only useful
 * over the graph canvas, so TopologyPage mounts it; framed pages
 * (Resources, Snapshots) don't render it.
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
        left: 'var(--atlas-space-4)',
        bottom: 'var(--atlas-space-4)',
        pointerEvents: 'none',
        color: 'var(--atlas-text-2)',
        opacity: 0.7,
        zIndex: 4,
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

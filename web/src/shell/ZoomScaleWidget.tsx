/* ============================================================
 * ZoomScaleWidget — bottom-right compass scale + level picker.
 *
 * Reads the canvas's current Cytoscape zoom (passed down from
 * TopologyView via its onZoom callback) and shows the L-band it
 * falls into per the design's zoom continuum (L1 cluster / L2
 * namespace / L3 workload / L4 resource). The four level chips are
 * clickable — picking one animates the canvas to that level's
 * target zoom (the FLIP split/merge of aggregated nodes belongs to
 * a later pass; this widget makes the continuum visible and
 * controllable today).
 *
 * Mockup: design/views/03-zoom-continuum.svg — see the bottom-right
 * "0.25× L1 · 3 clusters" chip.
 * ============================================================ */
import { Box, Stack, Typography } from '@mui/material';

import { Panel } from '../design';

// Zoom thresholds that map the cytoscape scalar to a level band.
// Picked so the default fit lands in L2 (cluster overview at 1.0×)
// and the operator can scroll out to L1 or in to L3/L4. The exact
// breakpoints match the design's table.
export const LEVEL_ZOOMS = [
  { level: 'L1', zoom: 0.25, label: 'cluster' },
  { level: 'L2', zoom: 1.0, label: 'namespace' },
  { level: 'L3', zoom: 2.5, label: 'workload' },
  { level: 'L4', zoom: 5.0, label: 'resource' },
] as const;

export type ZoomLevel = (typeof LEVEL_ZOOMS)[number]['level'];

export function levelForZoom(zoom: number): ZoomLevel {
  // Pick the highest band whose target zoom the current zoom meets
  // or exceeds. Defaults to L1 for very-zoomed-out views.
  let active: ZoomLevel = 'L1';
  for (const band of LEVEL_ZOOMS) {
    if (zoom >= band.zoom * 0.75) active = band.level;
  }
  return active;
}

interface ZoomScaleWidgetProps {
  zoom: number;
  nodeCount?: number;
  onPickLevel?: (zoom: number) => void;
}

export function ZoomScaleWidget({ zoom, nodeCount, onPickLevel }: ZoomScaleWidgetProps) {
  const active = levelForZoom(zoom);
  return (
    <Box
      sx={{
        position: 'absolute',
        right: 'var(--atlas-space-4)',
        bottom: 'var(--atlas-space-4)',
        zIndex: 4,
        pointerEvents: 'auto',
      }}
    >
      <Panel
        variant="card"
        padding={2}
        ariaLabel="Zoom scale"
        sx={{ display: 'flex', flexDirection: 'column', gap: 0.75, minWidth: 168 }}
      >
        <Box
          sx={{
            display: 'flex',
            alignItems: 'baseline',
            justifyContent: 'space-between',
            gap: 1,
          }}
        >
          <Typography
            component="span"
            sx={{
              fontFamily: 'var(--atlas-font-mono)',
              fontSize: 13,
              color: 'var(--atlas-text-1)',
              fontWeight: 600,
            }}
          >
            {zoom.toFixed(2)}×
          </Typography>
          <Typography
            component="span"
            sx={{
              fontFamily: 'var(--atlas-font-mono)',
              fontSize: 11,
              color: 'var(--atlas-text-3)',
            }}
          >
            {active}
            {nodeCount != null ? ` · ${nodeCount} nodes` : ''}
          </Typography>
        </Box>
        <Stack
          direction="row"
          spacing={0.5}
          role="group"
          aria-label="Zoom level"
        >
          {LEVEL_ZOOMS.map((band) => {
            const isActive = band.level === active;
            return (
              <Box
                key={band.level}
                component="button"
                type="button"
                onClick={() => onPickLevel?.(band.zoom)}
                aria-pressed={isActive}
                aria-label={`${band.level} ${band.label} (${band.zoom}×)`}
                sx={{
                  flex: 1,
                  padding: '4px 0',
                  background: isActive
                    ? 'color-mix(in srgb, var(--atlas-select) 18%, transparent)'
                    : 'transparent',
                  border: '1px solid var(--atlas-border)',
                  borderColor: isActive ? 'var(--atlas-select)' : 'var(--atlas-border)',
                  cursor: 'pointer',
                  fontFamily: 'var(--atlas-font-mono)',
                  fontSize: 11,
                  color: isActive ? 'var(--atlas-select)' : 'var(--atlas-text-2)',
                  '&:hover': {
                    borderColor: 'var(--atlas-select)',
                  },
                  '&:focus-visible': {
                    outline: '2px solid var(--atlas-select)',
                    outlineOffset: 1,
                  },
                }}
              >
                {band.level}
              </Box>
            );
          })}
        </Stack>
      </Panel>
    </Box>
  );
}

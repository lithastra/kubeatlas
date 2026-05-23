/* ============================================================
 * EdgeTypeFilter — preset chip group for "same graph, fewer edges".
 *
 * RBAC chain, network reachability, and configuration dependency
 * are not separate views in this design — they're filters of the
 * one graph. The chips here let the operator narrow the visible
 * edge set to a curated preset; edges outside it get the canvas
 * dim treatment and disconnected nodes follow.
 *
 * The presets are deliberately coarse — single-edge-type filters
 * could grow into a full pop-out later; the value is the IA
 * primitive (filter folds into the graph, not a route change).
 * ============================================================ */
import { Box, Stack, Typography } from '@mui/material';

import type { EdgeType } from '../api/types';

export type EdgeFilterPreset = 'all' | 'rbac' | 'network' | 'config' | 'storage';

export const EDGE_PRESET_LABEL: Record<EdgeFilterPreset, string> = {
  all: 'All',
  rbac: 'RBAC',
  network: 'Network',
  config: 'Config',
  storage: 'Storage',
};

// Which EdgeType strings each preset keeps visible. `all` is the
// pass-through; the others scope down to the matching domain.
export const EDGE_PRESET_TYPES: Record<EdgeFilterPreset, ReadonlySet<EdgeType> | null> = {
  all: null,
  rbac: new Set<EdgeType>(['USES_SERVICEACCOUNT', 'OWNS']),
  network: new Set<EdgeType>(['ROUTES_TO', 'SELECTS', 'SELECTS_NP', 'ALLOWS_FROM', 'ALLOWS_TO']),
  config: new Set<EdgeType>(['USES_CONFIGMAP', 'USES_SECRET']),
  storage: new Set<EdgeType>(['MOUNTS_VOLUME', 'ATTACHED_TO']),
};

interface EdgeTypeFilterProps {
  value: EdgeFilterPreset;
  onChange: (next: EdgeFilterPreset) => void;
}

export function EdgeTypeFilter({ value, onChange }: EdgeTypeFilterProps) {
  return (
    <Stack direction="row" spacing={0.5} alignItems="center" role="group" aria-label="Edge filter">
      <Typography
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 10,
          letterSpacing: '0.04em',
          color: 'var(--atlas-text-3)',
          textTransform: 'uppercase',
          mr: 0.5,
        }}
      >
        edges
      </Typography>
      {(Object.keys(EDGE_PRESET_LABEL) as EdgeFilterPreset[]).map((p) => {
        const isActive = p === value;
        return (
          <Box
            key={p}
            component="button"
            type="button"
            onClick={() => onChange(p)}
            aria-pressed={isActive}
            sx={{
              padding: '4px 8px',
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
            {EDGE_PRESET_LABEL[p]}
          </Box>
        );
      })}
    </Stack>
  );
}

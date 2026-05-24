/* ============================================================
 * LeftClusterStrip — 56px vertical column for multi-cluster mode.
 *
 * Wired to /api/v1/federation/clusters. Shows an "ALL" indicator
 * at the top followed by one square chip per cluster. Clicking a
 * chip selects that cluster as the focus; clicking the globe (or
 * the active chip again) returns to the all-clusters view. The
 * non-federated case (empty cluster list) collapses to a single
 * "local" chip so single-cluster installs still see the strip's
 * visual structure.
 *
 * The graph-fetch swap to /federation/graph based on the picked
 * cluster lands with the federation graph view; this strip makes
 * the IA visible and the selection state addressable today.
 * ============================================================ */
import { Box, Stack, Tooltip, Typography } from '@mui/material';

import { useFederationClusters } from '../api/federation';
import { Icon } from '../design';
import { clusterColour } from '../lib/clusterColour';
import { useClusterSelection } from './ClusterSelectionContext';

function clusterInitials(id: string): string {
  const parts = id.split(/[-_/]/).filter(Boolean);
  if (parts.length >= 2) {
    return (parts[0][0] + parts[1][0]).toUpperCase();
  }
  return id.slice(0, 2).toUpperCase();
}

export function LeftClusterStrip() {
  const { selected, setSelected } = useClusterSelection();
  const { data, isLoading } = useFederationClusters();
  const clusters = data?.clusters ?? [];
  const items = clusters.length > 0 ? clusters : ['local'];

  return (
    <Box
      role="region"
      aria-label="Cluster strip"
      sx={{
        width: 'var(--atlas-chrome-left-cluster-strip)',
        flexShrink: 0,
        backgroundColor: 'var(--atlas-surface)',
        borderInlineEnd: '1px solid var(--atlas-border)',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        paddingBlock: 'var(--atlas-space-3)',
        gap: 'var(--atlas-space-2)',
      }}
    >
      <Tooltip title={`All clusters (${items.length})`} placement="right">
        <Box
          component="button"
          type="button"
          onClick={() => setSelected(null)}
          aria-pressed={selected === null}
          aria-label="All clusters"
          sx={{
            width: 36,
            height: 36,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            border: '1.5px solid',
            borderColor:
              selected === null ? 'var(--atlas-select)' : 'var(--atlas-border)',
            backgroundColor:
              selected === null
                ? 'color-mix(in srgb, var(--atlas-select) 15%, transparent)'
                : 'transparent',
            cursor: 'pointer',
            color:
              selected === null ? 'var(--atlas-select)' : 'var(--atlas-text-2)',
            '&:hover': { borderColor: 'var(--atlas-select)' },
            '&:focus-visible': {
              outline: '2px solid var(--atlas-select)',
              outlineOffset: 1,
            },
          }}
        >
          <Icon name="compass" size={16} />
        </Box>
      </Tooltip>
      <Typography
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 9,
          color: 'var(--atlas-text-3)',
          letterSpacing: '0.04em',
        }}
      >
        ALL ({items.length})
      </Typography>
      <Box sx={{ width: 32, height: 1, backgroundColor: 'var(--atlas-border)' }} />

      <Stack alignItems="center" spacing={1.25}>
        {isLoading && (
          <Typography
            component="span"
            sx={{
              fontFamily: 'var(--atlas-font-mono)',
              fontSize: 9,
              color: 'var(--atlas-text-3)',
            }}
          >
            …
          </Typography>
        )}
        {items.map((id) => (
          <ClusterChip
            key={id}
            id={id}
            active={selected === id}
            onClick={() => setSelected(selected === id ? null : id)}
          />
        ))}
      </Stack>
    </Box>
  );
}

interface ClusterChipProps {
  id: string;
  active: boolean;
  onClick: () => void;
}

function ClusterChip({ id, active, onClick }: ClusterChipProps) {
  const color = clusterColour(id);
  return (
    <Tooltip title={id} placement="right">
      <Box
        component="button"
        type="button"
        onClick={onClick}
        aria-pressed={active}
        aria-label={`Focus cluster ${id}`}
        sx={{
          width: 28,
          height: 28,
          padding: 0,
          border: '1.5px solid',
          borderColor: active ? 'var(--atlas-select)' : 'transparent',
          background: color,
          cursor: 'pointer',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          color: '#F4EFE6',
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 10,
          fontWeight: 600,
          letterSpacing: '0.02em',
          '&:hover': { borderColor: 'var(--atlas-select)' },
          '&:focus-visible': {
            outline: '2px solid var(--atlas-select)',
            outlineOffset: 1,
          },
        }}
      >
        {clusterInitials(id)}
      </Box>
    </Tooltip>
  );
}

/* ============================================================
 * BlastRadiusPanel — right-panel summary while blast mode is active.
 *
 * Shows: total impacted count, per-hop breakdown, then the affected
 * resources grouped by hop. Mirrors the design's right panel for
 * the hero blast-radius view minus the action row — Slack / CSV
 * export / save-query are mutating or external-facing actions that
 * don't belong in a read-only explorer.
 * ============================================================ */
import { Box, Stack, Typography } from '@mui/material';

import type { View, ViewNode } from '../api/types';
import { Panel } from '../design';
import { computeBlastRadius, type BlastDirection } from '../lib/blastRadius';

interface BlastRadiusPanelProps {
  view: View | undefined;
  rootId: string;
  depth: number;
  direction: BlastDirection;
}

export function BlastRadiusPanel({ view, rootId, depth, direction }: BlastRadiusPanelProps) {
  if (!view) {
    return (
      <Typography variant="caption" sx={{ color: 'var(--atlas-text-3)' }}>
        Graph not loaded.
      </Typography>
    );
  }
  const result = computeBlastRadius(view, rootId, direction, depth);
  const nodesById = new Map(view.nodes.map((n) => [n.id, n]));
  const hops = Array.from(result.byHop.entries())
    .filter(([h]) => h > 0)
    .sort(([a], [b]) => a - b);
  const directHop = result.byHop.get(1)?.length ?? 0;
  const transitive = result.reachable.size - 1 - directHop;

  return (
    <Stack spacing={2}>
      <Box>
        <Typography
          component="div"
          sx={{
            fontFamily: 'var(--atlas-font-display)',
            fontSize: 'var(--atlas-text-heading-size)',
            color: 'var(--atlas-text-1)',
          }}
        >
          Blast Radius
        </Typography>
        <Typography
          component="div"
          sx={{
            fontFamily: 'var(--atlas-font-mono)',
            fontSize: 11,
            color: 'var(--atlas-text-3)',
            mt: 0.5,
            wordBreak: 'break-all',
          }}
        >
          Root: {rootId} · {direction} · depth {depth === Infinity ? '∞' : depth}
        </Typography>
      </Box>

      <Stack direction="row" spacing={1.5}>
        <SummaryStat label="Total impacted" value={result.reachable.size - 1} />
        <SummaryStat label="Direct (1 hop)" value={directHop} />
        <SummaryStat label="Transitive" value={transitive} />
      </Stack>

      {hops.length === 0 && (
        <Typography variant="caption" sx={{ color: 'var(--atlas-text-3)' }}>
          No reachable neighbours within the depth limit.
        </Typography>
      )}

      {hops.map(([hop, ids]) => (
        <Panel
          key={hop}
          variant="card"
          padding={3}
          ariaLabel={`Hop ${hop}`}
        >
          <Box
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: 1,
              mb: 1,
            }}
          >
            <Box
              sx={{
                px: 1,
                py: 0.25,
                backgroundColor: 'var(--atlas-select)',
                color: 'var(--atlas-bg)',
                fontFamily: 'var(--atlas-font-mono)',
                fontSize: 10,
              }}
            >
              {hop} hop
            </Box>
            <Typography
              component="span"
              sx={{
                fontFamily: 'var(--atlas-font-mono)',
                fontSize: 11,
                color: 'var(--atlas-text-3)',
              }}
            >
              {ids.length} {ids.length === 1 ? 'resource' : 'resources'}
            </Typography>
          </Box>
          <Stack spacing={0.5}>
            {ids.slice(0, 30).map((id) => (
              <NodeRow key={id} node={nodesById.get(id)} id={id} />
            ))}
            {ids.length > 30 && (
              <Typography variant="caption" sx={{ color: 'var(--atlas-text-3)' }}>
                + {ids.length - 30} more
              </Typography>
            )}
          </Stack>
        </Panel>
      ))}
    </Stack>
  );
}

function SummaryStat({ label, value }: { label: string; value: number }) {
  return (
    <Box
      sx={{
        flex: 1,
        padding: 'var(--atlas-space-2) var(--atlas-space-3)',
        border: '1px solid var(--atlas-border)',
      }}
    >
      <Typography
        component="div"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 10,
          letterSpacing: '0.04em',
          color: 'var(--atlas-text-3)',
          textTransform: 'uppercase',
        }}
      >
        {label}
      </Typography>
      <Typography
        component="div"
        sx={{
          fontFamily: 'var(--atlas-font-display)',
          fontSize: 22,
          color: 'var(--atlas-text-1)',
          lineHeight: 1.1,
          mt: 0.25,
        }}
      >
        {value}
      </Typography>
    </Box>
  );
}

function NodeRow({ node, id }: { node: ViewNode | undefined; id: string }) {
  if (!node) {
    return (
      <Box
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 11,
          color: 'var(--atlas-text-3)',
          wordBreak: 'break-all',
        }}
      >
        {id}
      </Box>
    );
  }
  return (
    <Box
      sx={{
        fontFamily: 'var(--atlas-font-mono)',
        fontSize: 11,
        color: 'var(--atlas-text-2)',
        wordBreak: 'break-all',
      }}
    >
      <Box component="span" sx={{ color: 'var(--atlas-text-3)' }}>
        {node.kind ?? '?'} ·{' '}
      </Box>
      {node.namespace ? `${node.namespace}/` : ''}
      <Box component="span" sx={{ color: 'var(--atlas-text-1)' }}>
        {node.name ?? node.label ?? id}
      </Box>
    </Box>
  );
}

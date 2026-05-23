/* ============================================================
 * DiffChangeLog — right-panel content while diff mode is active.
 *
 * Renders the summary cards (added / deleted / modified counts) and
 * a chronological list of DiffEntry rows. Surfaces the Tier-1 503
 * as a "snapshots not enabled" message rather than a generic error
 * — diff requires the PostgreSQL store.
 *
 * Mirrors the design's right-panel for view 06 minus the Save /
 * Copy / Share action row — those are out of scope for a read-only
 * explorer and would need export wiring.
 * ============================================================ */
import { Box, CircularProgress, Stack, Typography } from '@mui/material';

import { ApiError } from '../api/client';
import { useSnapshotDiff } from '../api/snapshots';
import type { DiffEntry } from '../api/types';
import { Panel } from '../design';

interface DiffChangeLogProps {
  anchor: string;
  namespace?: string;
}

export function DiffChangeLog({ anchor, namespace = '' }: DiffChangeLogProps) {
  const { data, isLoading, isError, error } = useSnapshotDiff(anchor, 'now', namespace);

  if (isLoading) {
    return (
      <Stack direction="row" alignItems="center" spacing={1}>
        <CircularProgress size={14} />
        <Typography variant="caption">Loading diff…</Typography>
      </Stack>
    );
  }

  if (isError) {
    const status = error instanceof ApiError ? error.status : 0;
    if (status === 503) {
      return (
        <Typography variant="caption" sx={{ color: 'var(--atlas-text-3)' }}>
          Snapshots aren&apos;t enabled on this install. Diff requires the Tier 2
          PostgreSQL store.
        </Typography>
      );
    }
    return (
      <Typography variant="caption" sx={{ color: 'var(--atlas-error)' }}>
        Diff failed: {(error as Error)?.message ?? 'unknown error'}
      </Typography>
    );
  }

  if (!data) return null;

  const added = data.added ?? [];
  const removed = data.removed ?? [];
  const modified = data.modified ?? [];
  const allRows = [
    ...added.map((e) => ({ ...e, kind_: 'added' as const })),
    ...removed.map((e) => ({ ...e, kind_: 'removed' as const })),
    ...modified.map((e) => ({ ...e, kind_: 'modified' as const })),
  ].sort((a, b) => (a.ts < b.ts ? 1 : -1)); // newest first

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
          Changes since {anchor} ago
        </Typography>
        <Typography
          component="div"
          sx={{
            fontFamily: 'var(--atlas-font-mono)',
            fontSize: 11,
            color: 'var(--atlas-text-3)',
            mt: 0.5,
          }}
        >
          {data.from} → {data.to}
        </Typography>
      </Box>

      <Stack direction="row" spacing={1.5}>
        <DiffStat label="Added" color="var(--atlas-healthy, #5C7F6B)" value={added.length} />
        <DiffStat label="Deleted" color="var(--atlas-error, #A14638)" value={removed.length} />
        <DiffStat label="Modified" color="var(--atlas-warning, #B8893A)" value={modified.length} />
      </Stack>

      {allRows.length === 0 ? (
        <Typography variant="caption" sx={{ color: 'var(--atlas-text-3)' }}>
          No changes in this window.
        </Typography>
      ) : (
        <Stack spacing={1}>
          {allRows.slice(0, 100).map((row) => (
            <DiffRow key={`${row.kind_}-${row.namespace}/${row.kind}/${row.name}-${row.ts}`} row={row} />
          ))}
          {allRows.length > 100 && (
            <Typography variant="caption" sx={{ color: 'var(--atlas-text-3)' }}>
              + {allRows.length - 100} more
            </Typography>
          )}
        </Stack>
      )}
    </Stack>
  );
}

function DiffStat({ label, value, color }: { label: string; value: number; color: string }) {
  return (
    <Box
      sx={{
        flex: 1,
        padding: 'var(--atlas-space-2) var(--atlas-space-3)',
        border: `1px solid ${color}`,
        backgroundColor: `color-mix(in srgb, ${color} 10%, transparent)`,
      }}
    >
      <Typography
        component="div"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 10,
          letterSpacing: '0.04em',
          color,
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

type DiffRowKind = 'added' | 'removed' | 'modified';

const ROW_META: Record<
  DiffRowKind,
  { color: string; tag: string; prefix: string }
> = {
  added: { color: 'var(--atlas-healthy, #5C7F6B)', tag: 'ADDED', prefix: '+' },
  removed: { color: 'var(--atlas-error, #A14638)', tag: 'DELETED', prefix: '−' },
  modified: { color: 'var(--atlas-warning, #B8893A)', tag: 'MODIFIED', prefix: '↻' },
};

function DiffRow({ row }: { row: DiffEntry & { kind_: DiffRowKind } }) {
  const meta = ROW_META[row.kind_];
  return (
    <Panel variant="card" padding={2} ariaLabel={`${meta.tag} ${row.namespace}/${row.name}`}>
      <Box sx={{ display: 'flex', gap: 1 }}>
        <Box sx={{ width: 3, alignSelf: 'stretch', backgroundColor: meta.color, flexShrink: 0 }} />
        <Box sx={{ flexGrow: 1, minWidth: 0 }}>
          <Typography
            component="div"
            sx={{
              fontFamily: 'var(--atlas-font-mono)',
              fontSize: 10,
              letterSpacing: '0.04em',
              color: meta.color,
              textTransform: 'uppercase',
            }}
          >
            {meta.prefix} {meta.tag} · {formatRelative(row.ts)}
          </Typography>
          <Typography
            component="div"
            sx={{
              fontFamily: 'var(--atlas-font-mono)',
              fontSize: 13,
              color: 'var(--atlas-text-1)',
              mt: 0.5,
              wordBreak: 'break-all',
              textDecoration: row.kind_ === 'removed' ? 'line-through' : 'none',
            }}
          >
            {row.namespace ? `${row.namespace}/` : ''}
            {row.name}
          </Typography>
          <Typography
            component="div"
            sx={{
              fontFamily: 'var(--atlas-font-mono)',
              fontSize: 11,
              color: 'var(--atlas-text-3)',
              mt: 0.25,
            }}
          >
            {row.kind}
          </Typography>
        </Box>
      </Box>
    </Panel>
  );
}

function formatRelative(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const deltaSec = Math.max(0, (Date.now() - t) / 1000);
  if (deltaSec < 60) return `${Math.round(deltaSec)}s ago`;
  if (deltaSec < 3600) return `${Math.round(deltaSec / 60)}m ago`;
  if (deltaSec < 86400) return `${Math.round(deltaSec / 3600)}h ago`;
  return `${Math.round(deltaSec / 86400)}d ago`;
}

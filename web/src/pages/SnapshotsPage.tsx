import { useMemo, useState } from 'react';
import {
  Alert,
  Box,
  Chip,
  CircularProgress,
  Divider,
  Link as MuiLink,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  ToggleButton,
  ToggleButtonGroup,
  Typography,
} from '@mui/material';
import { useTranslation } from 'react-i18next';
import { Link as RouterLink } from 'react-router-dom';

import { ApiError } from '../api/client';
import { useSnapshotDiff, useSnapshots } from '../api/snapshots';
import type { DiffEntry, EventType, SnapshotMeta } from '../api/types';

// SnapshotsPage answers "what changed in the last X minutes / hours
// / days?". It is Tier 2 only — when the server returns 503 the page
// shows a single "snapshots not enabled" banner instead of the
// timeline and diff. Single-cluster operators on Tier 1 still get a
// useful explanation rather than a generic error.
//
// Layout:
//   - top: a fixed set of relative-window presets + an optional
//     namespace input. The wire form ("5m" / "1h" / "24h" / "7d")
//     passes straight through to the server.
//   - middle: the diff result with three sub-tables (Added / Removed
//     / Modified). Each row links to the resource detail page for
//     the resource's current state; the diff itself never carries
//     full payloads.
//   - bottom: the snapshot_meta timeline (most-recent first) so the
//     operator can see when the last periodic full sync ran and how
//     wide the queryable window currently is.
export function SnapshotsPage() {
  const { t } = useTranslation('translation');

  const windowPresets = ['5m', '1h', '24h', '7d'] as const;
  const [from, setFrom] = useState<(typeof windowPresets)[number]>('1h');
  const [namespace, setNamespace] = useState('');

  const snapshots = useSnapshots();
  const diff = useSnapshotDiff(from, 'now', namespace);

  // 503 from either endpoint means snapshots aren't enabled on this
  // server — both queries share the dedicated banner, no point
  // showing two stacked errors.
  const notEnabled =
    (snapshots.error instanceof ApiError && snapshots.error.status === 503) ||
    (diff.error instanceof ApiError && diff.error.status === 503);

  if (notEnabled) {
    return (
      <Stack spacing={2}>
        <Typography variant="h4">{t('page.snapshots.title')}</Typography>
        <Alert severity="info">{t('page.snapshots.notEnabled')}</Alert>
      </Stack>
    );
  }

  return (
    <Stack spacing={3}>
      <Typography variant="h4">{t('page.snapshots.title')}</Typography>
      <Typography variant="body2" color="text.secondary">
        {t('page.snapshots.subtitle')}
      </Typography>

      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2} alignItems="center">
        <ToggleButtonGroup
          value={from}
          exclusive
          size="small"
          onChange={(_, next) => {
            if (next) setFrom(next);
          }}
          aria-label={t('page.snapshots.windowLabel') ?? 'window'}
        >
          {windowPresets.map((w) => (
            <ToggleButton key={w} value={w}>
              {w}
            </ToggleButton>
          ))}
        </ToggleButtonGroup>
        <TextField
          size="small"
          label={t('page.snapshots.namespaceLabel')}
          value={namespace}
          placeholder={t('page.snapshots.namespacePlaceholder') ?? ''}
          onChange={(e) => setNamespace(e.target.value)}
        />
      </Stack>

      <DiffSection diff={diff} from={from} namespace={namespace} />

      <Divider />

      <TimelineSection snapshots={snapshots} />
    </Stack>
  );
}

interface DiffSectionProps {
  diff: ReturnType<typeof useSnapshotDiff>;
  from: string;
  namespace: string;
}

function DiffSection({ diff, from, namespace }: DiffSectionProps) {
  const { t } = useTranslation('translation');

  if (diff.isPending) {
    return (
      <Box display="flex" alignItems="center" gap={1}>
        <CircularProgress size={18} />
        <Typography variant="body2" color="text.secondary">
          {t('page.snapshots.diffLoading')}
        </Typography>
      </Box>
    );
  }
  if (diff.isError && !(diff.error instanceof ApiError && diff.error.status === 503)) {
    return <Alert severity="error">{(diff.error as Error).message}</Alert>;
  }
  if (!diff.data) return null;

  const total =
    diff.data.added.length + diff.data.removed.length + diff.data.modified.length;
  return (
    <Stack spacing={2}>
      <Typography variant="h6">
        {t('page.snapshots.diffHeader', {
          count: total,
          window: from,
          namespace: namespace || t('page.snapshots.allNamespaces'),
        })}
      </Typography>
      <DiffTable entries={diff.data.added} eventType="add" />
      <DiffTable entries={diff.data.modified} eventType="update" />
      <DiffTable entries={diff.data.removed} eventType="delete" />
    </Stack>
  );
}

interface DiffTableProps {
  entries: DiffEntry[];
  eventType: EventType;
}

function DiffTable({ entries, eventType }: DiffTableProps) {
  const { t } = useTranslation('translation');
  const heading = t(`page.snapshots.eventHeader.${eventType}`, {
    count: entries.length,
  });
  if (entries.length === 0) {
    return (
      <Box>
        <Typography variant="subtitle2">{heading}</Typography>
        <Typography variant="body2" color="text.secondary">
          {t('page.snapshots.emptySection')}
        </Typography>
      </Box>
    );
  }
  return (
    <Box>
      <Typography variant="subtitle2" gutterBottom>
        {heading}
      </Typography>
      <TableContainer component={Paper} variant="outlined">
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>{t('page.snapshots.col.namespace')}</TableCell>
              <TableCell>{t('page.snapshots.col.kind')}</TableCell>
              <TableCell>{t('page.snapshots.col.name')}</TableCell>
              <TableCell>{t('page.snapshots.col.when')}</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {entries.map((e) => (
              <DiffRow key={`${e.namespace}/${e.kind}/${e.name}/${e.ts}`} entry={e} />
            ))}
          </TableBody>
        </Table>
      </TableContainer>
    </Box>
  );
}

function DiffRow({ entry }: { entry: DiffEntry }) {
  // Removed resources cannot be linked to /resources/... — that page
  // would 404. We render plain text for delete rows; add and update
  // rows link to the current resource detail.
  const isRemoved = entry.eventType === 'delete';
  const target = `/resources/${encodeURIComponent(entry.namespace || '_')}/${encodeURIComponent(
    entry.kind,
  )}/${encodeURIComponent(entry.name)}`;
  return (
    <TableRow hover>
      <TableCell>{entry.namespace || '—'}</TableCell>
      <TableCell>{entry.kind}</TableCell>
      <TableCell>
        {isRemoved ? (
          entry.name
        ) : (
          <MuiLink component={RouterLink} to={target} underline="hover">
            {entry.name}
          </MuiLink>
        )}
      </TableCell>
      <TableCell>{formatTimestamp(entry.ts)}</TableCell>
    </TableRow>
  );
}

function TimelineSection({ snapshots }: { snapshots: ReturnType<typeof useSnapshots> }) {
  const { t } = useTranslation('translation');

  const rows: SnapshotMeta[] = useMemo(() => snapshots.data?.snapshots ?? [], [snapshots.data]);

  if (snapshots.isPending) {
    return (
      <Box display="flex" alignItems="center" gap={1}>
        <CircularProgress size={18} />
        <Typography variant="body2" color="text.secondary">
          {t('page.snapshots.timelineLoading')}
        </Typography>
      </Box>
    );
  }
  if (snapshots.isError && !(snapshots.error instanceof ApiError && snapshots.error.status === 503)) {
    return <Alert severity="error">{(snapshots.error as Error).message}</Alert>;
  }
  return (
    <Stack spacing={1}>
      <Typography variant="h6">{t('page.snapshots.timelineHeader')}</Typography>
      {rows.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          {t('page.snapshots.timelineEmpty')}
        </Typography>
      ) : (
        <TableContainer component={Paper} variant="outlined">
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>{t('page.snapshots.col.when')}</TableCell>
                <TableCell>{t('page.snapshots.col.trigger')}</TableCell>
                <TableCell align="right">{t('page.snapshots.col.resources')}</TableCell>
                <TableCell align="right">{t('page.snapshots.col.edges')}</TableCell>
                <TableCell align="right">{t('page.snapshots.col.duration')}</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.map((m) => (
                <TableRow key={m.id} hover>
                  <TableCell>{formatTimestamp(m.ts)}</TableCell>
                  <TableCell>
                    <Chip size="small" label={m.trigger} />
                  </TableCell>
                  <TableCell align="right">{m.resourceCount.toLocaleString()}</TableCell>
                  <TableCell align="right">{m.edgeCount.toLocaleString()}</TableCell>
                  <TableCell align="right">{formatDuration(m.durationMs)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Stack>
  );
}

// formatTimestamp renders an RFC3339 string in the operator's local
// timezone, short form. Falls back to the raw string for inputs the
// browser cannot parse so we never blank a row.
function formatTimestamp(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return ts;
  return d.toLocaleString();
}

// formatDuration renders a millisecond count as a short "1.2s" /
// "850ms" string for the timeline.
function formatDuration(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(ms >= 10_000 ? 0 : 1)}s`;
  return `${ms}ms`;
}

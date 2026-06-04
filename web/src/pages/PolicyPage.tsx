import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Box,
  Chip,
  CircularProgress,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  ToggleButton,
  ToggleButtonGroup,
  Typography,
} from '@mui/material';

import { useConstraintAffected, useConstraints } from '../api/policy';
import type { PolicyConstraint } from '../api/types';

const ENGINES = ['all', 'gatekeeper', 'kyverno'] as const;

// PolicyPage lists Gatekeeper / Kyverno policy constraints and, for a
// selected one, the resources it enforces with their violation status.
// Read-only — KubeAtlas observes the policy engines, it does not edit
// policies.
export function PolicyPage() {
  const { t } = useTranslation('translation');
  const [engine, setEngine] = useState<string>('all');
  const [selected, setSelected] = useState<string>('');

  const constraints = useConstraints(engine === 'all' ? undefined : engine);

  return (
    <Box sx={{ padding: 'var(--atlas-space-8)', width: '100%', overflow: 'auto' }}>
      <Stack spacing={3}>
        <Typography variant="h4">{t('page.policy.title')}</Typography>
        <Typography variant="body2" color="text.secondary">
          {t('page.policy.subtitle')}
        </Typography>

        <ToggleButtonGroup
          size="small"
          exclusive
          value={engine}
          onChange={(_, next) => {
            if (next) {
              setEngine(next);
              setSelected('');
            }
          }}
        >
          {ENGINES.map(e => (
            <ToggleButton key={e} value={e}>
              {t(`page.policy.engine.${e}`)}
            </ToggleButton>
          ))}
        </ToggleButtonGroup>

        {constraints.isPending && <CircularProgress size={20} />}
        {constraints.isError && (
          <Alert severity="error">{(constraints.error as Error).message}</Alert>
        )}
        {constraints.data && constraints.data.length === 0 && (
          <Alert severity="info">{t('page.policy.empty')}</Alert>
        )}
        {constraints.data && constraints.data.length > 0 && (
          <ConstraintsTable rows={constraints.data} selected={selected} onSelect={setSelected} />
        )}

        {selected && <AffectedSection name={selected} />}
      </Stack>
    </Box>
  );
}

function ConstraintsTable({
  rows,
  selected,
  onSelect,
}: {
  rows: PolicyConstraint[];
  selected: string;
  onSelect: (name: string) => void;
}) {
  const { t } = useTranslation('translation');
  return (
    <TableContainer component={Paper} variant="outlined">
      <Table size="small" aria-label={t('page.policy.title')}>
        <TableHead>
          <TableRow>
            <TableCell>{t('page.policy.col.name')}</TableCell>
            <TableCell>{t('page.policy.col.kind')}</TableCell>
            <TableCell>{t('page.policy.col.engine')}</TableCell>
            <TableCell align="right">{t('page.policy.col.violations')}</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {rows.map(r => (
            <TableRow
              key={`${r.engine}/${r.name}`}
              hover
              selected={selected === r.name}
              onClick={() => onSelect(r.name)}
              sx={{ cursor: 'pointer' }}
            >
              <TableCell>{r.name}</TableCell>
              <TableCell>{r.kind}</TableCell>
              <TableCell>{r.engine}</TableCell>
              <TableCell align="right">
                <Chip
                  size="small"
                  color={r.violations > 0 ? 'error' : 'success'}
                  label={r.violations}
                />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

function AffectedSection({ name }: { name: string }) {
  const { t } = useTranslation('translation');
  const affected = useConstraintAffected(name);

  if (affected.isPending) {
    return <CircularProgress size={18} />;
  }
  if (affected.isError) {
    return <Alert severity="error">{(affected.error as Error).message}</Alert>;
  }
  if (!affected.data) {
    return null;
  }

  return (
    <Stack spacing={1}>
      <Typography variant="h6">
        {t('page.policy.affectedHeader', { name, count: affected.data.count })}
      </Typography>
      <TableContainer component={Paper} variant="outlined">
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>{t('page.policy.col.resource')}</TableCell>
              <TableCell>{t('page.policy.col.status')}</TableCell>
              <TableCell>{t('page.policy.col.message')}</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {affected.data.resources.map(a => (
              <TableRow key={`${a.resource.namespace}/${a.resource.kind}/${a.resource.name}`}>
                <TableCell>
                  {a.resource.namespace}/{a.resource.kind}/{a.resource.name}
                </TableCell>
                <TableCell>
                  <Chip
                    size="small"
                    color={a.violated ? 'error' : 'success'}
                    label={a.violated ? t('page.policy.violated') : t('page.policy.compliant')}
                  />
                </TableCell>
                <TableCell>{a.message ?? ''}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
    </Stack>
  );
}

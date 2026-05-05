import { useState } from 'react';
import { Alert, CircularProgress, Stack, Typography } from '@mui/material';
import { useTranslation } from 'react-i18next';

import { useGraph } from '../api/graph';
import type { Level } from '../api/types';
import { LevelTabs } from '../components/LevelTabs';
import { NamespacePicker } from '../components/NamespacePicker';
import { TopologyView } from '../components/TopologyView';
import { useAppSelector } from '../store';

// TopologyPage drives the Cytoscape view. Cluster level shows one
// node per namespace plus cross-ns edges; namespace level shows the
// per-workload aggregation inside the picked namespace. Workload /
// resource levels are reachable from the resource detail page in
// P1-T15 (left disabled here until then).
export function TopologyPage() {
  const { t } = useTranslation('translation');
  const [level, setLevel] = useState<Level>('cluster');
  const namespace = useAppSelector((s) => s.filter.namespace);

  // useGraph decides whether to fire based on completeness of the
  // scope params — at cluster level it fires unconditionally, at
  // namespace level it waits for a namespace pick.
  const params =
    level === 'cluster'
      ? { level: 'cluster' as const }
      : { level: 'namespace' as const, namespace: namespace ?? undefined };

  const { data, isLoading, isError, error } = useGraph(params);

  let body: React.ReactNode;
  if (level === 'namespace' && !namespace) {
    body = <Alert severity="info">{t('filter.namespace.all')}</Alert>;
  } else if (isLoading) {
    body = (
      <Stack alignItems="center" sx={{ py: 4 }}>
        <CircularProgress size={24} />
      </Stack>
    );
  } else if (isError) {
    body = <Alert severity="error">{(error as Error)?.message ?? 'unknown error'}</Alert>;
  } else {
    body = <TopologyView view={data} />;
  }

  return (
    <Stack spacing={2}>
      <Typography variant="h4">{t('page.topology.title')}</Typography>
      <LevelTabs value={level} onChange={setLevel} disableWorkload disableResource />
      {level === 'namespace' && <NamespacePicker />}
      {body}
    </Stack>
  );
}

import { Stack, Typography } from '@mui/material';
import { useTranslation } from 'react-i18next';

import { NamespacePicker } from '../components/NamespacePicker';
import { ResourceTable } from '../components/ResourceTable';

// ResourcesPage is the v0.1.0 default landing page: namespace
// dropdown on top, table below. Selecting a namespace fires the
// namespace-level aggregation; clicking a row routes into the
// resource detail view (implemented in P1-T15).
export function ResourcesPage() {
  const { t } = useTranslation('translation');
  return (
    <Stack spacing={2}>
      <Typography variant="h4">{t('page.resources.title')}</Typography>
      <NamespacePicker />
      <ResourceTable />
    </Stack>
  );
}

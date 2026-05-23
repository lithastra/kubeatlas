import { Box, Stack, Typography } from '@mui/material';
import { useTranslation } from 'react-i18next';

import { NamespacePicker } from '../components/NamespacePicker';
import { ResourceTable } from '../components/ResourceTable';

// ResourcesPage is the v0.1.0 default landing page: namespace
// dropdown on top, table below. Selecting a namespace fires the
// namespace-level aggregation; clicking a row routes into the
// resource detail view.
//
// Outer Box owns the page padding so the content doesn't butt up
// against the left cluster strip. TopologyPage handles its own
// layout (full-bleed absolute positioning) so the shell doesn't
// impose padding globally.
export function ResourcesPage() {
  const { t } = useTranslation('translation');
  return (
    <Box sx={{ padding: 'var(--atlas-space-5)', width: '100%', overflow: 'auto' }}>
      <Stack spacing={2}>
        <Typography variant="h4">{t('page.resources.title')}</Typography>
        <NamespacePicker />
        <ResourceTable />
      </Stack>
    </Box>
  );
}

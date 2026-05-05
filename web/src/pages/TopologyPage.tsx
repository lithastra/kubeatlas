import { Stack, Typography } from '@mui/material';
import { useTranslation } from 'react-i18next';

// Placeholder. Cytoscape topology view lands in P1-T13.
export function TopologyPage() {
  const { t } = useTranslation('translation');
  return (
    <Stack spacing={1}>
      <Typography variant="h4">{t('page.topology.title')}</Typography>
      <Typography variant="body2" color="text.secondary">
        {t('page.topology.placeholder')}
      </Typography>
    </Stack>
  );
}

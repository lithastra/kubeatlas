import { Stack, Typography } from '@mui/material';
import { useTranslation } from 'react-i18next';

// Placeholder. The real page (DataGrid + namespace filter) lands in
// P1-T12.
export function ResourcesPage() {
  const { t } = useTranslation('translation');
  return (
    <Stack spacing={1}>
      <Typography variant="h4">{t('page.resources.title')}</Typography>
      <Typography variant="body2" color="text.secondary">
        {t('page.resources.placeholder')}
      </Typography>
    </Stack>
  );
}

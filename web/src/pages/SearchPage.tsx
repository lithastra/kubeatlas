import { Stack, Typography } from '@mui/material';
import { useTranslation } from 'react-i18next';

// Placeholder. Search UI is part of the resources / topology
// integration in later P1 tasks; the API endpoint already exists at
// /api/v1alpha1/search.
export function SearchPage() {
  const { t } = useTranslation('translation');
  return (
    <Stack spacing={1}>
      <Typography variant="h4">{t('page.search.title')}</Typography>
      <Typography variant="body2" color="text.secondary">
        {t('page.search.placeholder')}
      </Typography>
    </Stack>
  );
}

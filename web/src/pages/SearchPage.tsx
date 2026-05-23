import { Box, Stack, Typography } from '@mui/material';
import { useTranslation } from 'react-i18next';

// Placeholder. Live search now lives in the topology canvas's ⌘K
// command palette (M5.2). This route exists for nav consistency.
export function SearchPage() {
  const { t } = useTranslation('translation');
  return (
    <Box sx={{ padding: 'var(--atlas-space-5)', width: '100%', overflow: 'auto' }}>
      <Stack spacing={1}>
        <Typography variant="h4">{t('page.search.title')}</Typography>
        <Typography variant="body2" color="text.secondary">
          {t('page.search.placeholder')}
        </Typography>
      </Stack>
    </Box>
  );
}

import { Link, Stack, Typography } from '@mui/material';
import { useTranslation } from 'react-i18next';

// External-link page. The full docs live on the Docusaurus site;
// this page exists so the nav drawer has somewhere to point.
export function DocsPage() {
  const { t } = useTranslation('translation');
  const { t: tApp } = useTranslation('app');
  return (
    <Stack spacing={2}>
      <Typography variant="h4">{t('page.docs.title')}</Typography>
      <Typography variant="body1">{t('page.docs.intro')}</Typography>
      <Link href={tApp('docs.url')} target="_blank" rel="noopener noreferrer" variant="h6">
        {tApp('docs.label')}
      </Link>
    </Stack>
  );
}

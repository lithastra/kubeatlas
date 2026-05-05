import { Link, Stack, Typography } from '@mui/material';

// External-link page. The full docs live on the Docusaurus site;
// this page exists so the nav drawer has somewhere to point.
export function DocsPage() {
  return (
    <Stack spacing={2}>
      <Typography variant="h4">Documentation</Typography>
      <Typography variant="body1">
        The full KubeAtlas docs — quick start, architecture, roadmap, developer
        guide — live at:
      </Typography>
      <Link
        href="https://docs.kubeatlas.lithastra.com"
        target="_blank"
        rel="noopener noreferrer"
        variant="h6"
      >
        docs.kubeatlas.lithastra.com
      </Link>
    </Stack>
  );
}

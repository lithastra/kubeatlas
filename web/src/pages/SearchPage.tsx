import { Stack, Typography } from '@mui/material';

// Placeholder. Search UI is part of the resources / topology
// integration in later P1 tasks; the API endpoint already exists at
// /api/v1alpha1/search.
export function SearchPage() {
  return (
    <Stack spacing={1}>
      <Typography variant="h4">Search</Typography>
      <Typography variant="body2" color="text.secondary">
        Cross-namespace resource search — coming alongside the resources page.
      </Typography>
    </Stack>
  );
}

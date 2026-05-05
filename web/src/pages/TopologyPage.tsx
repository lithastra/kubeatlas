import { Stack, Typography } from '@mui/material';

// Placeholder. Cytoscape topology view lands in P1-T13.
export function TopologyPage() {
  return (
    <Stack spacing={1}>
      <Typography variant="h4">Topology</Typography>
      <Typography variant="body2" color="text.secondary">
        Cytoscape graph at cluster / namespace / workload levels — coming in P1-T13.
      </Typography>
    </Stack>
  );
}

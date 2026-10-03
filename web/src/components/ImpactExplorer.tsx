import { useState } from 'react';
import { Box, Button, MenuItem, Stack, TextField, Typography } from '@mui/material';

import { useImpact, type ImpactTarget } from '../api/impact';
import type { ImpactRelation } from '../api/impactTypes';
import { BlastRadiusPanel } from './BlastRadiusPanel';

// Mount under a resource-identity key so navigation never inherits an open
// analysis or an expected UID from the previous resource.
export function ImpactExplorer({ target }: { target: ImpactTarget }) {
  const [open, setOpen] = useState(false);
  const [relation, setRelation] = useState<ImpactRelation>('dependents');
  const [depth, setDepth] = useState(5);
  const query = useImpact(open ? target : null, relation, depth);
  return (
    <Box sx={{ my: 3, maxWidth: 760 }}>
      <Button variant="outlined" onClick={() => setOpen((value) => !value)} aria-expanded={open}>
        {open ? 'Close impact analysis' : 'Analyze impact'}
      </Button>
      {open ? <Stack spacing={2} sx={{ mt: 2 }}>
        <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
          <TextField select label="Relation" value={relation} onChange={(event) => setRelation(event.target.value as ImpactRelation)} size="small" sx={{ minWidth: 0, flex: 1 }}>
            <MenuItem value="dependents">Dependents — resources that depend on this</MenuItem>
            <MenuItem value="dependencies">Dependencies — resources this depends on</MenuItem>
          </TextField>
          <TextField select label="Maximum depth" value={depth} onChange={(event) => setDepth(Number(event.target.value))} size="small" sx={{ minWidth: 130 }}>
            {[1, 2, 3, 5, 10].map((n) => <MenuItem key={n} value={n}>{n}</MenuItem>)}
          </TextField>
        </Stack>
        {!target.expectedUID ? <Typography variant="caption">No expected UID is available; this query is not pinned to a previously observed instance.</Typography> : null}
        <BlastRadiusPanel response={query.data} loading={query.isFetching} error={query.error} onRefresh={() => { void query.refetch(); }} />
      </Stack> : null}
    </Box>
  );
}

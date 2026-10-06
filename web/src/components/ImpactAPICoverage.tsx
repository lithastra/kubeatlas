import { Box, Stack, Typography } from '@mui/material';

import type { ImpactAPIEndpoint, ImpactCoverage, ImpactInventorySnapshot } from '../api/impactTypes';

function endpointName(endpoint: ImpactAPIEndpoint): string {
  return [endpoint.group, endpoint.version, endpoint.resource].filter(Boolean).join('/');
}

function presenceName(state: string): string {
  switch (state) {
    case 'advertised': return 'Advertised';
    case 'not_advertised': return 'Not advertised';
    case 'other_version_advertised': return 'Other version advertised';
    case 'unsupported_shape': return 'Unsupported endpoint shape';
    default: return 'Unknown';
  }
}

function inventoryName(state: string): string {
  switch (state) {
    case 'complete': return 'Complete enumeration';
    case 'partial': return 'Partial enumeration';
    case 'permission_denied': return 'Discovery permission denied';
    case 'failed': return 'Discovery failed';
    default: return 'Unknown';
  }
}

function Inventory({ title, snapshot }: { title: string; snapshot?: ImpactInventorySnapshot }) {
  const inventory = snapshot?.apiInventory;
  return <Box>
    <Typography component="h4" variant="subtitle2">{title}</Typography>
    {inventory ? <>
      <Typography variant="body2">
        {inventoryName(inventory.state)}{inventory.stale ? ' · stale' : ''}{inventory.stopped ? ' · stopped' : ''}{inventory.limited ? ' · limited' : ''}
      </Typography>
      <Typography variant="caption" component="p">Checked: {inventory.checkedAt && !inventory.checkedAt.startsWith('0001-') ? inventory.checkedAt : 'none observed / unknown'}</Typography>
      <Typography variant="caption" component="p">Captured endpoint descriptors: {inventory.resources?.length || 0}</Typography>
    </> : <Typography variant="body2">Inventory unavailable</Typography>}
  </Box>;
}

// Presentation of the validated capture only. Backend presence assessments and
// before/after flags remain independent of current time and canvas filters.
export function ImpactAPICoverage({ coverage }: { coverage: ImpactCoverage }) {
  return <Box component="details" sx={{ minWidth: 0, overflowWrap: 'anywhere' }}>
    <summary>API observation details</summary>
    <Stack spacing={2} sx={{ mt: 1 }}>
      <Typography variant="body2">Captured API metadata, not successful watches or effective permissions. Complete enumeration does not establish complete graph observation.</Typography>
      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, minmax(0, 1fr))' }, gap: 2 }}>
        <Inventory title="Before graph read" snapshot={coverage.before} />
        <Inventory title="After graph read" snapshot={coverage.after} />
      </Box>
      <Box>
        <Typography component="h4" variant="subtitle2">Optional APIs</Typography>
        {coverage.optionalApis?.length ? <Box component="ul" sx={{ pl: 2, my: 1 }}>
          {coverage.optionalApis.map((api) => <Box component="li" key={endpointName(api)} sx={{ mb: 1 }}>
            <Typography variant="body2">{endpointName(api)}</Typography>
            <Typography variant="caption" component="p">Before: {presenceName(api.before)} · After: {presenceName(api.after)}</Typography>
          </Box>)}
        </Box> : <Typography variant="body2">Optional API assessments are unavailable.</Typography>}
        <Typography variant="caption" component="p">Not advertised refers to the captured discovery scope, not an uninstalled operator. Unknown, other versions and unsupported shapes remain limitations.</Typography>
      </Box>
      <Box>
        <Typography component="h4" variant="subtitle2">Advertised endpoints without captured observation</Typography>
        {coverage.unobservedApis?.length ? <Box component="ul" sx={{ pl: 2, my: 1, maxHeight: 240, overflowY: 'auto' }}>
          {coverage.unobservedApis.map((api) => <Box component="li" key={endpointName(api)} sx={{ mb: 1 }}>
            <Typography variant="body2">{endpointName(api)}</Typography>
            <Typography variant="caption" component="p">{api.kind} · {api.namespaced ? 'namespaced' : 'cluster-scoped'}</Typography>
          </Box>)}
        </Box> : <Typography variant="body2">No unobserved endpoints are listed in this capture. This does not establish complete collector coverage.</Typography>}
        <Typography variant="caption" component="p">These are resource types, not object counts. Missing registration evidence does not prove no collector exists.</Typography>
      </Box>
    </Stack>
  </Box>;
}

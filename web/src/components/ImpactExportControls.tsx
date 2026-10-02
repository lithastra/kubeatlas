import { useState } from 'react';
import { Alert, Button, Checkbox, FormControlLabel, Stack, Typography } from '@mui/material';

import { isImpactCapture } from '../api/impactCapture';
import type { ImpactResponse } from '../api/impactTypes';
import { downloadImpactCapture, type ImpactExportFormat } from '../lib/impactExport';

export function ImpactExportControls({ response }: { response: ImpactResponse }) {
  const [approved, setApproved] = useState<ImpactResponse | null>(null);
  const [notice, setNotice] = useState<{ response: ImpactResponse; failed: boolean } | null>(null);
  const enabled = approved === response && isImpactCapture(response);
  function download(format: ImpactExportFormat) {
    if (!enabled) return;
    try {
      downloadImpactCapture(response, format);
      setNotice({ response, failed: false });
    } catch {
      setNotice({ response, failed: true });
    }
  }
  return <Stack spacing={1} aria-label="Export captured analysis">
    <Typography component="h3" variant="subtitle2">Export this captured result</Typography>
    <Typography variant="body2">Downloads use the result shown here, without another query. Files contain sensitive cluster topology; share only with authorized recipients.</Typography>
    <FormControlLabel control={<Checkbox checked={approved === response} onChange={(_, checked) => { setApproved(checked ? response : null); setNotice(null); }} />}
      label="I understand this file contains sensitive cluster topology." />
    <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1}>
      <Button variant="outlined" disabled={!enabled} onClick={() => download('json')}>Download JSON</Button>
      <Button variant="outlined" disabled={!enabled} onClick={() => download('html')}>Download HTML</Button>
    </Stack>
    {notice?.response === response ? <Alert severity={notice.failed ? 'error' : 'info'} role="status">
      {notice.failed ? 'Could not prepare this download. No new analysis was requested.' : 'Download requested. Your browser controls whether and where the file is saved.'}
    </Alert> : null}
  </Stack>;
}

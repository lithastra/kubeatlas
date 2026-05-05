import { Alert, CircularProgress, Stack } from '@mui/material';
import { useParams } from 'react-router-dom';

import { useGraph, useResource } from '../api/graph';
import { ResourceDetailPanel } from '../components/ResourceDetailPanel';

// ResourcePage handles /resources/:namespace/:kind/:name. It fires
// two queries in parallel: the detail bundle (resource + edges) and
// the resource-level View (which carries the server-rendered Mermaid
// text). The detail bundle alone is enough to render the panel; the
// View is best-effort additive.
export function ResourcePage() {
  const params = useParams<{ namespace: string; kind: string; name: string }>();
  // Map the URL '_' sentinel back to "" so client-side display
  // matches the server's notion of cluster-scoped resources.
  const namespace = params.namespace === '_' ? '' : (params.namespace ?? '');
  const kind = params.kind ?? '';
  const name = params.name ?? '';

  const detail = useResource({ namespace, kind, name });
  const view = useGraph({ level: 'resource', namespace, kind, name });

  if (detail.isLoading) {
    return (
      <Stack alignItems="center" sx={{ py: 4 }}>
        <CircularProgress size={24} />
      </Stack>
    );
  }
  if (detail.isError || !detail.data) {
    return <Alert severity="error">{(detail.error as Error)?.message ?? 'failed to load resource'}</Alert>;
  }

  return (
    <ResourceDetailPanel
      resource={detail.data.resource}
      incoming={detail.data.incoming}
      outgoing={detail.data.outgoing}
      mermaidText={view.data?.mermaid}
    />
  );
}

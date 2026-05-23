/* ============================================================
 * NodeDetailPanel — content for the right context slot when a
 * topology node is selected.
 *
 * Renders: cartography heading (Kind / Namespace · Name), the
 * status pill, and 1-hop neighbour lists (incoming / outgoing
 * edges) — the minimum the v1.3 mockup calls for. The richer
 * blast-radius / time-axis affordances land in their own M5
 * sub-steps.
 *
 * Resolves the resource by id from the resource-detail endpoint.
 * Falls back to the bare id when the resource isn't yet cached.
 * ============================================================ */
import { Box, CircularProgress, Stack, Typography } from '@mui/material';

import { useResource } from '../api/graph';
import { Panel, StatusPill } from '../design';

interface NodeDetailPanelProps {
  /** graph.Resource.ID() — namespace/kind/name (cluster-prefixed in
   *  multicluster mode). */
  nodeId: string;
}

export function NodeDetailPanel({ nodeId }: NodeDetailPanelProps) {
  const parsed = parseNodeId(nodeId);
  const { data, isLoading, isError } = useResource({
    namespace: parsed.namespace ?? null,
    kind: parsed.kind ?? null,
    name: parsed.name ?? null,
  });

  return (
    <Stack spacing={2}>
      <Header parsed={parsed} />
      {isLoading && (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
          <CircularProgress size={14} />
          <Typography variant="caption">Loading details…</Typography>
        </Box>
      )}
      {isError && (
        <Typography variant="caption" sx={{ color: 'var(--atlas-error)' }}>
          Could not load resource details.
        </Typography>
      )}
      {data && (
        <>
          <Stack direction="row" spacing={1}>
            <StatusPill variant="healthy" />
          </Stack>
          <Panel variant="card" padding={3} ariaLabel="Incoming edges">
            <Typography
              component="div"
              sx={{ fontFamily: 'var(--atlas-font-display)', fontSize: 14, mb: 1 }}
            >
              Incoming · {data.incoming?.length ?? 0}
            </Typography>
            <EdgeList edges={data.incoming ?? []} />
          </Panel>
          <Panel variant="card" padding={3} ariaLabel="Outgoing edges">
            <Typography
              component="div"
              sx={{ fontFamily: 'var(--atlas-font-display)', fontSize: 14, mb: 1 }}
            >
              Outgoing · {data.outgoing?.length ?? 0}
            </Typography>
            <EdgeList edges={data.outgoing ?? []} />
          </Panel>
        </>
      )}
    </Stack>
  );
}

function Header({ parsed }: { parsed: ParsedNodeID }) {
  return (
    <Box>
      <Typography
        component="div"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 'var(--atlas-text-caption-size)',
          color: 'var(--atlas-text-3)',
        }}
      >
        {parsed.namespace ? `${parsed.namespace} / ` : ''}
        {parsed.kind}
      </Typography>
      <Typography
        component="div"
        sx={{
          fontFamily: 'var(--atlas-font-display)',
          fontSize: 'var(--atlas-text-heading-size)',
          color: 'var(--atlas-text-1)',
          lineHeight: 1.2,
        }}
      >
        {parsed.name || parsed.id}
      </Typography>
    </Box>
  );
}

interface EdgeRow {
  from?: string;
  to?: string;
  type?: string;
}

function EdgeList({ edges }: { edges: EdgeRow[] }) {
  if (edges.length === 0) {
    return (
      <Typography
        component="div"
        sx={{ fontSize: 'var(--atlas-text-caption-size)', color: 'var(--atlas-text-3)' }}
      >
        no edges
      </Typography>
    );
  }
  return (
    <Stack spacing={1}>
      {edges.slice(0, 30).map((e, i) => (
        <Box
          key={`${e.from}-${e.to}-${e.type}-${i}`}
          sx={{
            fontFamily: 'var(--atlas-font-mono)',
            fontSize: 'var(--atlas-text-caption-size)',
            color: 'var(--atlas-text-2)',
            wordBreak: 'break-all',
          }}
        >
          <Box component="span" sx={{ color: 'var(--atlas-text-1)' }}>
            {e.type}
          </Box>{' '}
          → {e.to ?? e.from}
        </Box>
      ))}
      {edges.length > 30 && (
        <Typography variant="caption" sx={{ color: 'var(--atlas-text-3)' }}>
          + {edges.length - 30} more
        </Typography>
      )}
    </Stack>
  );
}

interface ParsedNodeID {
  id: string;
  clusterId?: string;
  namespace?: string;
  kind?: string;
  name?: string;
}

function parseNodeId(id: string): ParsedNodeID {
  // ID grammar: [<clusterID>:]<namespace>/<kind>/<name>
  let clusterId: string | undefined;
  let rest = id;
  const colon = id.indexOf(':');
  // A colon BEFORE the first slash means the multicluster prefix.
  if (colon > -1 && colon < id.indexOf('/')) {
    clusterId = id.slice(0, colon);
    rest = id.slice(colon + 1);
  }
  const parts = rest.split('/');
  if (parts.length === 3) {
    const [namespace, kind, name] = parts;
    return { id, clusterId, namespace: namespace || undefined, kind, name };
  }
  return { id, clusterId };
}

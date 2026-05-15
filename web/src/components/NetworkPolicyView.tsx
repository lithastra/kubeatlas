import { Alert, Box, CircularProgress, Stack, Typography } from '@mui/material';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';

import { useNetworkPolicyAllowGraph, useNetworkPolicySelected } from '../api/graph';
import type { Resource } from '../api/types';

export interface NetworkPolicyViewProps {
  // The NetworkPolicy resource the detail page is rendering. The
  // parent (ResourceDetailPanel) only mounts this component when
  // resource.kind === 'NetworkPolicy', but we still gate the hooks
  // on `enabled` so a stray mount can't fire bogus requests.
  namespace: string;
  name: string;
}

// NetworkPolicyView is the F-109 "NetworkPolicy view" the Phase 3
// spec asks for on the single-resource detail page. It surfaces
// three lists the backend derives from the policy's podSelector and
// ingress/egress peers:
//
//   - Selected   : Pods / workloads spec.podSelector matches
//   - Allow from : declared ingress sources (spec.ingress[].from[])
//   - Allow to   : declared egress destinations (spec.egress[].to[])
//
// It is declarative topology only — it shows what the policy spec
// says, never what the CNI actually enforces.
export function NetworkPolicyView({ namespace, name }: NetworkPolicyViewProps) {
  const selected = useNetworkPolicySelected(namespace, name, true);
  const allow = useNetworkPolicyAllowGraph(namespace, name, true);

  const loading = selected.isLoading || allow.isLoading;
  const error = selected.error ?? allow.error;

  return (
    <Box data-testid="networkpolicy-view">
      <Typography variant="h6" sx={{ mb: 1 }}>
        NetworkPolicy
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Declared selector + ingress/egress peers. Reflects the policy spec, not CNI enforcement.
      </Typography>

      {loading && (
        <Stack alignItems="center" sx={{ py: 2 }}>
          <CircularProgress size={20} />
        </Stack>
      )}

      {!loading && error && (
        <Alert severity="error">{(error as Error).message}</Alert>
      )}

      {!loading && !error && (
        <Stack direction={{ xs: 'column', md: 'row' }} spacing={3}>
          <ResourceList title="Selected" resources={selected.data?.selected ?? []} />
          <ResourceList title="Allow from" resources={allow.data?.allowFrom ?? []} />
          <ResourceList title="Allow to" resources={allow.data?.allowTo ?? []} />
        </Stack>
      )}
    </Box>
  );
}

interface ResourceListProps {
  title: string;
  resources: Resource[];
}

// ResourceList renders one of the three columns. Each row navigates
// to the target resource's own detail page on click — so a user can
// walk podSelector -> Pod -> the Pod's other edges in one flow.
function ResourceList({ title, resources }: ResourceListProps) {
  const navigate = useNavigate();
  const { t: tg } = useTranslation('glossary');

  return (
    <Box sx={{ flex: 1, minWidth: 0 }}>
      <Typography variant="subtitle2" sx={{ mb: 1 }}>
        {title} ({resources.length})
      </Typography>
      {resources.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          —
        </Typography>
      ) : (
        <Stack spacing={0.5}>
          {resources.map((r) => {
            const ns = r.namespace || '_';
            return (
              <Box
                key={`${r.namespace}/${r.kind}/${r.name}`}
                onClick={() => navigate(`/resources/${ns}/${r.kind}/${r.name}`)}
                sx={{
                  cursor: 'pointer',
                  fontFamily: 'monospace',
                  fontSize: 12,
                  '&:hover': { textDecoration: 'underline' },
                }}
              >
                {tg(`kind.${r.kind}`, { defaultValue: r.kind })}/{r.name}
                {r.namespace ? '' : ' (cluster-scoped)'}
              </Box>
            );
          })}
        </Stack>
      )}
    </Box>
  );
}

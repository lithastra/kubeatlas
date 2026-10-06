import { Alert, Box, Button, Stack, Typography } from '@mui/material';

import { impactErrorMessage } from '../api/impact';
import type { ImpactCoverage, ImpactFacet, ImpactResponse } from '../api/impactTypes';
import { ImpactAPICoverage } from './ImpactAPICoverage';
import { ImpactExportControls } from './ImpactExportControls';

interface BlastRadiusPanelProps {
  response?: ImpactResponse;
  loading: boolean;
  error: unknown;
  onRefresh: () => void;
}

export function BlastRadiusPanel({ response, loading, error, onRefresh }: BlastRadiusPanelProps) {
  return (
    <Stack spacing={2} sx={{ overflowWrap: 'anywhere', minWidth: 0 }} aria-label="Impact analysis">
      <Typography component="h2" variant="h6">Impact analysis</Typography>
      <Typography variant="body2">
        Current observed relationships, not an outage prediction or a safe-to-delete assessment.
        Canvas filters do not restrict this analysis. Counts exclude the root.
      </Typography>
      <Button variant="outlined" onClick={onRefresh} disabled={loading}>Refresh analysis</Button>
      {loading ? <Typography role="status">Loading server analysis…</Typography> : error ? (
        <Alert severity="error">{impactErrorMessage(error)}</Alert>
      ) : response ? <ImpactResult response={response} /> : (
        <Alert severity="info">Select a concrete resource in the current cluster to analyze.</Alert>
      )}
    </Stack>
  );
}

function ImpactResult({ response }: { response: ImpactResponse }) {
  const a = response.analysis;
  const availability = a.availability;
  const history = availability?.evidence.history;
  const retention = history?.retentionEvidence;
  return (
    <>
      <Box>
        <Typography variant="subtitle2">{a.root.kind}/{a.root.name}</Typography>
        <Typography variant="body2">Cluster: {response.scope.clusterId || 'standalone'} · All namespaces</Typography>
        <Typography variant="body2">{a.relation} · Depth {a.maxDepth} · Limit {a.limit}</Typography>
        <Typography variant="caption" component="p">Captured: {response.generatedAt} · KubeAtlas {response.kubeatlasVersion}</Typography>
        <Typography variant="caption" component="p">UID: {a.root.uid || 'unknown / reference only'} · Resource version: {a.root.resourceVersion || 'unknown'}</Typography>
        {a.root.referenceOnly ? <Alert severity="info">Reference only. Existence and values are not verified.</Alert> : null}
      </Box>
      <ImpactExportControls response={response} />
      <Facet title="Observed relationships" facet={a} coverage={a.observation?.ordinary} relation={a.relation} />
      {a.observation ? <ImpactAPICoverage coverage={a.observation.ordinary} /> : null}
      <details>
        <summary>Reference field evidence ({a.referenceEvidence.length})</summary>
        <Typography variant="body2">Optional does not mean harmless; refresh behavior does not prove application reload.</Typography>
        {a.referenceEvidence.map((ref) => (
          <Box key={JSON.stringify([ref.from, ref.to, ref.type])} sx={{ mt: 1 }}>
            <Typography variant="body2">{ref.from} → {ref.to} · {ref.type} · {ref.status}</Typography>
            <Typography variant="caption">Source UID: {ref.sourceUID || 'unknown'} · Version: {ref.sourceResourceVersion || 'unknown'} {ref.reason}</Typography>
            {ref.uses.map((use, i) => (
              <Typography key={i} variant="body2">{use.fieldPath} {use.mountFieldPath} · {use.mode} · {use.optional} · {use.refresh}</Typography>
            ))}
          </Box>
        ))}
      </details>
      <details>
        <summary>Authorization associations (separate)</summary>
        <Typography variant="body2">Not effective permissions, credential use, or ordinary impact counts.</Typography>
        {a.authorization.applicable ? <Facet title="Authorization" facet={a.authorization} coverage={a.observation?.authorization} relation="associations" /> : (
          <Typography variant="body2">Not applicable: {a.authorization.reason || 'unsupported root'}.</Typography>
        )}
      </details>
      <Box>
        <Typography component="h3" variant="subtitle2">Storage and history</Typography>
        <Typography variant="body2">Current graph read: {availability?.currentGraphRead || 'unknown'} · Background storage probe: {availability?.evidence.storage.state || 'unknown'}</Typography>
        <Typography variant="body2">History: {history?.state || 'unknown'} · Coverage: {history?.coverage || 'unknown'} · Retained metadata: {history?.retainedData || 'not queried'}</Typography>
        {history?.reasons.length ? <Typography variant="caption">{history.reasons.join(', ')}</Typography> : null}
        {retention ? (
          <Box sx={{ mt: 1 }}>
            <Typography variant="body2">Retention check: {retention.state}{retention.stale ? ' · stale' : ''}{retention.stopped ? ' · stopped' : ''}</Typography>
            <Typography variant="caption" component="p">Checked: {observedTime(retention.checkedAt)}</Typography>
            {retention.state === 'observed' ? <>
              <Typography variant="caption" component="p">Queried interval: {retention.bounds.from} — {retention.bounds.to}</Typography>
              <Typography variant="body2">First retained event: {observedTime(retention.bounds.firstEventAt)}</Typography>
              <Typography variant="body2">Last retained event: {observedTime(retention.bounds.lastEventAt)}</Typography>
              <Typography variant="body2">Latest marker record: {observedTime(retention.bounds.latestMarkerAt)}</Typography>
            </> : null}
          </Box>
        ) : null}
        <Typography variant="caption" component="p">Metadata only. Retained timestamps do not prove a continuous history interval. A marker record is not a verified backup or recovery point.</Typography>
      </Box>
      <Alert severity="warning">{response.sharingWarning}</Alert>
    </>
  );
}

function observedTime(value: string): string {
  return value && !value.startsWith('0001-') ? value : 'none observed / unknown';
}

function Facet({ title, facet, coverage, relation }: { title: string; facet: ImpactFacet; coverage?: ImpactCoverage; relation: string }) {
  const closedEmpty = facet.modeledTraversalComplete && !facet.truncated &&
    coverage?.typeScopeClosed && coverage.stableWindow &&
    coverage.emptyResultAssessment === 'none_found_in_analyzed_scope';
  return (
    <Stack spacing={1}>
      <Typography component="h3" variant="subtitle2">{title}</Typography>
      <Typography variant="body1">{facet.counts.lowerBound ? 'At least ' : ''}{facet.counts.total} observed · {facet.counts.direct} direct · {facet.counts.indirect} indirect</Typography>
      <Typography variant="body2">Observation: {facet.observationCoverage} · Type scope: {coverage?.typeScopeClosed ? 'closed' : 'not closed'}</Typography>
      {coverage?.reasons.length ? <Alert severity="warning">Observation limits: {coverage.reasons.join(', ')}</Alert> : null}
      {facet.truncated ? <Alert severity="warning">Truncated: {facet.truncationReasons.join(', ')}. Counts are lower bounds.</Alert> : null}
      {facet.notices.map((notice) => <Typography variant="caption" key={notice.reason}>{notice.reason}: {notice.count}</Typography>)}
      {!facet.resources.length ? (
        <Alert severity={closedEmpty ? 'info' : 'warning'}>
          {closedEmpty ? `No ${relation} found in the analyzed scope.` : `No ${relation} observed; analysis incomplete.`}
        </Alert>
      ) : null}
      {facet.resources.map((match) => (
        <Box component="details" key={match.resource.id} sx={{ border: '1px solid var(--atlas-border)', p: 1 }}>
          <summary>{match.resource.kind}/{match.resource.name} · {match.resource.namespace || 'cluster-scoped'} · {match.depth === 1 ? 'Direct' : `Indirect (${match.depth} hops)`}{match.resource.referenceOnly ? ' · reference only' : ''}</summary>
          <Typography variant="caption" component="p">One representative shortest path; not every possible path.</Typography>
          <Box component="ol" sx={{ pl: 2, m: 0 }}>
            {match.path.map((step, i) => (
              <Box component="li" key={i} sx={{ mb: 1 }}>
                <Typography variant="body2">{step.traversalFrom} → {step.traversalTo}</Typography>
                <Typography variant="caption">{step.type} · Stored edge: {step.from} → {step.to}{step.ownerUIDStatus ? ` · Owner UID: ${step.ownerUIDStatus}` : ''}{step.bindingScope ? ` · Binding scope: ${step.bindingScope} ${step.bindingNamespace || ''}` : ''}</Typography>
              </Box>
            ))}
          </Box>
        </Box>
      ))}
    </Stack>
  );
}

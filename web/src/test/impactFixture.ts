import type { ImpactResponse } from '../api/impactTypes';

// Synthetic wire fixture, not a cluster snapshot or release acceptance result.
export function impactFixture(clusterId = ''): ImpactResponse {
  const prefix = clusterId ? `${clusterId}:` : '';
  const root = { id: `${prefix}demo/ConfigMap/settings`, clusterId, namespace: 'demo', kind: 'ConfigMap', name: 'settings', uid: 'fixture-uid', resourceVersion: '10', referenceOnly: false };
  const pod = { id: `${prefix}demo/Pod/api`, clusterId, namespace: 'demo', kind: 'Pod', name: 'api', referenceOnly: false };
  const service = { id: `${prefix}demo/Service/api`, clusterId, namespace: 'demo', kind: 'Service', name: 'api', referenceOnly: false };
  const first = { from: pod.id, to: root.id, traversalFrom: root.id, traversalTo: pod.id, type: 'USES_CONFIGMAP' };
  const second = { from: service.id, to: pod.id, traversalFrom: pod.id, traversalTo: service.id, type: 'SELECTS' };
  const snapshot = () => ({ clusterId, generation: 1, capturedAt: '2026-10-01T00:00:00Z', state: 'partial', resources: [], sources: [],
    apiInventory: { clusterId, generation: 1, revision: 1, state: 'partial', checkedAt: '2026-10-01T00:00:00Z', stale: false, stopped: false, limited: false,
      resources: [{ group: 'example.test', version: 'v1', resource: 'widgets', kind: 'Widget', namespaced: true, list: true, watch: true }] } });
  const coverage = { profile: 'ordinary_minimum_v1', state: 'partial', typeScopeClosed: false, stableWindow: true, emptyResultAssessment: 'not_empty', reasons: ['dynamic_source_scope_unverified'], before: snapshot(), after: snapshot() };
  const result = {
    schemaVersion: '1', kubeatlasVersion: '1.7.0-dev', generatedAt: '2026-10-01T00:00:00Z',
    scope: { clusterId, mode: clusterId ? 'federated' : 'single', namespaceScope: 'all_namespaces_in_selected_cluster' },
    sharingWarning: 'Contains sensitive cluster topology. Review access and sharing permissions before exporting. Relationships are observations, not a safety or outage guarantee.',
    analysis: {
      root, relation: 'dependents', maxDepth: 5, limit: 200,
      resources: [{ resource: pod, depth: 1, path: [first] }, { resource: service, depth: 2, path: [first, second] }],
      counts: { total: 2, direct: 1, indirect: 1, workloads: 0, pods: 1, other: 1, lowerBound: true },
      truncated: false, truncationReasons: [], modeledTraversalComplete: true, observationCoverage: 'partial', notices: [],
      referenceEvidence: [{ from: pod.id, to: root.id, type: first.type, status: 'version_bound', sourceUID: 'fixture-pod', sourceResourceVersion: '11', uses: [{ fieldPath: '/spec/containers/0/envFrom/0/configMapRef', mode: 'envFrom', optional: 'required', refresh: 'container_start' }] }],
      authorization: { applicable: false, reason: 'unsupported_authorization_root', evidenceCategory: 'stored_edge_association', maxDepth: 5, limit: 200, effectivePermissions: 'not_evaluated', sourceVersionEvidence: 'unknown', resources: [], counts: { total: 0, direct: 0, indirect: 0, roles: 0, bindings: 0, serviceAccounts: 0, workloads: 0, pods: 0, lowerBound: false }, truncated: false, truncationReasons: [], notices: [], modeledTraversalComplete: true, observationCoverage: 'unknown' },
      observation: { ordinary: coverage, authorization: { ...coverage } },
      availability: { currentGraphRead: 'succeeded', evidence: {
        clusterId, capturedAt: '2026-10-01T00:00:00Z', storage: { state: 'available', durabilityKnown: true, durable: true, checkedAt: '2026-10-01T00:00:00Z', lastSuccessAt: '2026-10-01T00:00:00Z', staleAfterNanos: 600000000000 },
        history: { state: 'recording', reasons: [], observationStartedAt: '2026-09-30T00:00:00Z', retentionNanos: 604800000000000, content: 'metadata_only', coverage: 'unknown', retainedData: 'observed', retentionEvidence: {
          state: 'observed', checkedAt: '2026-10-01T00:00:00Z', stale: false, stopped: false,
          bounds: { clusterId, from: '2026-09-24T00:00:00Z', to: '2026-10-01T00:00:00Z', firstEventAt: '2026-09-30T23:00:00Z', lastEventAt: '2026-09-30T23:59:00Z', latestMarkerAt: '0001-01-01T00:00:00Z' },
        } },
      } },
    },
  };
  return result as ImpactResponse;
}

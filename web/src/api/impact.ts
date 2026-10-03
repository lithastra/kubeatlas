import { useQuery } from '@tanstack/react-query';

import { ApiError } from './client';
import { fetchImpactCapture } from './impactCapture';
import type { ImpactRelation, ImpactResponse } from './impactTypes';

export interface ImpactTarget {
  id: string;
  clusterId: string;
  namespace: string;
  kind: string;
  name: string;
  expectedUID?: string;
}

// IDs, not the current canvas filter, select one exact resource and cluster.
// Aggregated nodes must be rejected by the caller even if their IDs look valid.
export function parseImpactTarget(id: string, expectedUID?: string): ImpactTarget | null {
  let rest = id;
  let clusterId = '';
  const colon = id.indexOf(':');
  if (colon >= 0 && colon < id.indexOf('/')) {
    clusterId = id.slice(0, colon);
    rest = id.slice(colon + 1);
    if (!clusterId) return null;
  }
  const parts = rest.split('/');
  if (parts.length !== 3 || !parts[1] || !parts[2] || /[\\\s\x00-\x1f]/.test(id)) return null;
  const [ns, kind, name] = parts;
  if (ns.includes(':') || kind.includes(':') || clusterId.includes(',')) return null;
  const namespace = ns === '_' ? '' : ns;
  const canonicalID = `${clusterId ? `${clusterId}:` : ''}${namespace}/${kind}/${name}`;
  return { id: canonicalID, clusterId, namespace, kind, name, expectedUID };
}

export function impactURL(target: ImpactTarget, relation: ImpactRelation, depth: number): string {
  const query = new URLSearchParams({ relation, max_depth: String(depth), limit: '200' });
  if (target.clusterId) query.set('cluster', target.clusterId);
  if (target.expectedUID) query.set('expected_uid', target.expectedUID);
  const path = [target.namespace || '_', target.kind, target.name].map(encodeURIComponent).join('/');
  return `/api/v1/impact/${path}?${query}`;
}

// One captured result, explicitly refreshed. Never replay old-cluster or
// old-direction data, retry authorization errors, or poll the expensive query.
export function useImpact(target: ImpactTarget | null, relation: ImpactRelation, depth: number) {
  return useQuery({
    queryKey: ['impact', target?.id, target?.expectedUID, relation, depth],
    enabled: target !== null,
    queryFn: async ({ signal }) => {
      if (!target) throw new Error('No resource selected');
      const result = await fetchImpactCapture(impactURL(target, relation, depth), { signal });
      if (result.schemaVersion !== '1' || result.scope.clusterId !== target.clusterId ||
          result.analysis.root.id !== target.id || result.analysis.relation !== relation ||
          result.analysis.maxDepth !== depth || result.analysis.limit !== 200 ||
          (target.expectedUID && result.analysis.root.uid !== target.expectedUID)) {
        throw new Error('Impact response identity mismatch');
      }
      return result;
    },
    retry: false,
    // Preserve the exact object bound to its original JSON, rather than
    // constructing a structurally shared object that has no capture binding.
    structuralSharing: false,
    staleTime: Infinity,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}

export function impactErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    switch (error.status) {
      case 401: return 'Authentication is required for this cluster.';
      case 403: return 'Access to this cluster is not authorized.';
      case 404: return 'Resource, cluster, or impact API not found. No local estimate is substituted.';
      case 409: return 'Resource identity changed or cannot be verified. Re-select the resource before analyzing again.';
      case 413: return 'Analysis exceeds the server size limit. Choose a smaller depth.';
      case 429: return 'Analysis capacity is busy. Try again after the current requests finish.';
      case 503: return 'Impact evidence is unavailable.';
      case 504: return 'Impact analysis exceeded its deadline. Try a smaller depth.';
    }
  }
  return 'Could not obtain a matching impact result. No analysis is shown.';
}

export function impactHighlight(result?: ImpactResponse) {
  const nodes = new Set<string>();
  const edges = new Set<string>();
  if (result) {
    nodes.add(result.analysis.root.id);
    for (const match of result.analysis.resources) {
      nodes.add(match.resource.id);
      for (const step of match.path) edges.add(impactEdgeKey(step.from, step.to, step.type));
    }
  }
  return { nodes, edges };
}

export function impactEdgeKey(from: string, to: string, type: string): string {
  return JSON.stringify([from, to, type]);
}

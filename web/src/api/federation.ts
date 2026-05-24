/* ============================================================
 * federation — hooks for the /api/v1/federation/* endpoints.
 *
 * `useFederationClusters` lists the cluster IDs known to the
 * federation manager. On non-federated installs the endpoint
 * returns `{ clusters: [] }` (200 OK) — the UI treats that as
 * single-cluster mode and renders the strip with one "local" item.
 * ============================================================ */
import { useQuery, type UseQueryResult } from '@tanstack/react-query';

import { ApiError, fetchJSON } from './client';
import type { View, ViewEdge, ViewNode } from './types';

export interface FederationClustersResponse {
  clusters: string[];
}

// FederationNode mirrors pkg/aggregator.FederatedNode. Fields not
// relevant to a node's Type are absent.
interface FederationNode {
  id: string;
  type: 'resource' | 'cluster';
  clusterId: string;
  kind?: string;
  namespace?: string;
  name?: string;
  label?: string;
  resourceCount?: number;
  namespaceCount?: number;
  kindSummary?: Record<string, number>;
}

interface FederationView {
  level: string;
  clusters: string[];
  nodes: FederationNode[];
  edges: ViewEdge[];
}

const apiBase = '/api/v1';

export function useFederationClusters(): UseQueryResult<FederationClustersResponse, ApiError> {
  return useQuery<FederationClustersResponse, ApiError>({
    queryKey: ['federation-clusters'],
    queryFn: ({ signal }) =>
      fetchJSON<FederationClustersResponse>(`${apiBase}/federation/clusters`, { signal }),
    refetchInterval: 30_000,
    retry: (count, err) => {
      // 404 means the route isn't wired (older server) — don't retry.
      if (err instanceof ApiError && err.status === 404) return false;
      return count < 2;
    },
  });
}

export type FederationLevel = 'resource' | 'cluster';

export interface FederationGraphParams {
  clusters: string[];
  level?: FederationLevel;
}

// useFederationGraph wraps GET /api/v1/federation/graph?cluster=…
// Returns the response adapted to the same View shape the topology
// canvas already consumes (FederationNode → ViewNode with clusterId
// preserved on the node data), so TopologyView doesn't need a second
// code path for federated vs single-cluster fetches.
//
// 503 short-circuits the retry (federation is genuinely off — the
// caller should fall back to the regular /api/v1alpha1/graph hook).
// 400 also short-circuits because it usually means a stale cluster
// name in the picker that won't fix itself by retrying.
export function useFederationGraph(p: FederationGraphParams): UseQueryResult<View, ApiError> {
  const clusters = [...p.clusters].sort();
  const level: FederationLevel = p.level ?? 'resource';
  const enabled = clusters.length > 0;
  return useQuery<View, ApiError>({
    queryKey: ['federation-graph', clusters, level],
    enabled,
    queryFn: async ({ signal }) => {
      const qs = new URLSearchParams();
      qs.set('cluster', clusters.join(','));
      qs.set('level', level);
      const raw = await fetchJSON<FederationView>(
        `${apiBase}/federation/graph?${qs.toString()}`,
        { signal },
      );
      return federationViewToView(raw);
    },
    retry: (count, err) => {
      if (!(err instanceof ApiError)) return count < 2;
      if (err.status === 503 || err.status === 400) return false;
      return count < 2;
    },
  });
}

// federationViewToView projects the federation response onto the
// View shape the cytoscape adapter already speaks. The clusterId on
// each node is preserved as a data attribute so the stylesheet's
// `node[clusterId = "..."]` rule can pick it up.
function federationViewToView(raw: FederationView): View {
  const nodes: ViewNode[] = raw.nodes.map((n) => {
    if (n.type === 'cluster') {
      return {
        id: n.id,
        type: 'aggregated',
        level: 'cluster',
        label: n.label ?? n.clusterId,
        kind: 'Cluster',
        name: n.clusterId,
        children_count: n.resourceCount ?? 0,
        children_summary: n.kindSummary,
        edge_count_in: 0,
        edge_count_out: 0,
        // Non-standard but harmless: ViewNode is open via index sig
        // in spirit; cytoscape passes anything past the typed fields
        // straight through into n.data().
        clusterId: n.clusterId,
      } as ViewNode & { clusterId: string };
    }
    return {
      id: n.id,
      type: 'resource',
      kind: n.kind,
      namespace: n.namespace,
      name: n.name,
      label: n.kind && n.name ? `${n.kind}/${n.name}` : (n.name ?? n.id),
      edge_count_in: 0,
      edge_count_out: 0,
      clusterId: n.clusterId,
    } as ViewNode & { clusterId: string };
  });
  return {
    level: raw.level === 'cluster' ? 'cluster' : 'resource',
    nodes,
    edges: raw.edges,
  };
}

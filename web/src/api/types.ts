// Shared types mirroring the Go server's wire shapes
// (pkg/aggregator/aggregator.go and pkg/graph/model.go). Hand-written
// rather than generated from OpenAPI so the file stays small and
// readable; if the wire schema gets noticeably bigger, swap to a
// generator (openapi-typescript or similar).

export type Level = 'cluster' | 'namespace' | 'workload' | 'resource';

export type EdgeType =
  | 'OWNS'
  | 'USES_CONFIGMAP'
  | 'USES_SECRET'
  | 'MOUNTS_VOLUME'
  | 'SELECTS'
  | 'USES_SERVICEACCOUNT'
  | 'ROUTES_TO'
  | 'ATTACHED_TO';

export interface ViewNode {
  id: string;
  type: 'aggregated' | 'resource';
  level?: Level;
  label?: string;
  kind?: string;
  namespace?: string;
  name?: string;
  children_count?: number;
  children_summary?: Record<string, number>;
  edge_count_in: number;
  edge_count_out: number;
}

export interface ViewEdge {
  from: string;
  to: string;
  type?: EdgeType;
  count: number;
}

export interface View {
  level: Level;
  nodes: ViewNode[];
  edges: ViewEdge[];
  truncated?: boolean;
  // Server-rendered Mermaid flowchart text. Only ResourceAggregator
  // populates this (and only when nodes <= the renderer cap); empty
  // for cluster / namespace / workload levels.
  mermaid?: string;
}

export interface OwnerRef {
  kind: string;
  name: string;
  uid: string;
}

export interface Resource {
  kind: string;
  name: string;
  namespace: string;
  labels?: Record<string, string>;
  groupVersion?: string;
  uid?: string;
  annotations?: Record<string, string>;
  ownerReferences?: OwnerRef[];
  resourceVersion?: string;
}

export interface Edge {
  from: string;
  to: string;
  type: EdgeType;
}

export interface ResourceDetailResponse {
  resource: Resource;
  incoming: Edge[];
  outgoing: Edge[];
}

export interface SearchResponse {
  matches: Resource[];
  total: number;
  truncated: boolean;
}

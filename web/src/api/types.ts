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
  | 'ATTACHED_TO'
  // Phase 3 P3-T1 (F-109) NetworkPolicy edge types.
  | 'SELECTS_NP'
  | 'ALLOWS_FROM'
  | 'ALLOWS_TO';

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
  // Present only when search ran as an unindexed Tier 1 linear scan.
  warning?: string;
}

// One (value, frequency) pair within a LabelStat (F-114).
export interface LabelValue {
  value: string;
  count: number;
}

// Per-key summary from GET /api/v1/labels: the label key, how many
// resources carry it, and its most common values. `values` is capped
// server-side, so `valueCount` may exceed `values.length`.
export interface LabelStat {
  key: string;
  resourceCount: number;
  valueCount: number;
  values?: LabelValue[];
}

// Body of GET /api/v1/labels (F-114).
export interface LabelsResponse {
  labels: LabelStat[];
  count: number;
}

// Body of GET /api/v1/networkpolicy/{ns}/{name}/selected — the Pods
// and workloads a NetworkPolicy's spec.podSelector matches (F-109).
export interface NetworkPolicySelectedResponse {
  networkPolicy: Resource;
  selected: Resource[];
  count: number;
}

// Body of GET /api/v1/networkpolicy/{ns}/{name}/allow-graph — the
// declared ingress sources (allowFrom) and egress destinations
// (allowTo) of a NetworkPolicy (F-109).
export interface NetworkPolicyAllowGraphResponse {
  networkPolicy: Resource;
  allowFrom: Resource[];
  allowTo: Resource[];
}

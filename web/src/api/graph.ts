import { useQuery } from '@tanstack/react-query';

import { fetchJSON } from './client';
import type {
  Level,
  NetworkPolicyAllowGraphResponse,
  NetworkPolicySelectedResponse,
  ResourceDetailResponse,
  SearchResponse,
  View,
} from './types';

// Query params accepted by GET /api/v1alpha1/graph. Only `level` is
// always required; the rest depend on which level is being requested.
export interface GraphParams {
  level: Level;
  namespace?: string;
  kind?: string;
  name?: string;
  // F-114 label filter. Each key/value becomes a `label.<key>=<value>`
  // query param; the server AND-combines them. Honoured at cluster
  // and namespace level.
  labels?: Record<string, string>;
}

const apiBase = '/api/v1alpha1';

function graphURL(p: GraphParams): string {
  const q = new URLSearchParams({ level: p.level });
  if (p.namespace) q.set('namespace', p.namespace);
  if (p.kind) q.set('kind', p.kind);
  if (p.name) q.set('name', p.name);
  for (const [k, v] of Object.entries(p.labels ?? {})) {
    if (k && v) q.set(`label.${k}`, v);
  }
  return `${apiBase}/graph?${q.toString()}`;
}

// useGraph wraps GET /graph in React Query. Disabled when required
// scope params are missing so the hook can be called unconditionally.
export function useGraph(p: GraphParams) {
  const enabled = isScopeComplete(p);
  return useQuery<View>({
    queryKey: ['graph', p],
    queryFn: ({ signal }) => fetchJSON<View>(graphURL(p), { signal }),
    enabled,
  });
}

// useClusterGraph is a thin convenience for the namespace-picker use
// case (it doesn't need scope params).
export function useClusterGraph() {
  return useGraph({ level: 'cluster' });
}

// useNamespaceGraph is the resource-table case: the namespace must be
// selected for the query to fire.
export function useNamespaceGraph(namespace: string | null) {
  return useGraph({ level: 'namespace', namespace: namespace ?? undefined });
}

// Params for GET /api/v1alpha1/resources/{ns}/{kind}/{name}.
export interface ResourceParams {
  namespace: string | null;
  kind: string | null;
  name: string | null;
}

function resourceURL(p: ResourceParams): string {
  const ns = p.namespace || '_'; // sentinel for cluster-scoped resources
  return `${apiBase}/resources/${encodeURIComponent(ns)}/${encodeURIComponent(p.kind ?? '')}/${encodeURIComponent(p.name ?? '')}`;
}

// useResource fetches the resource detail bundle (the resource plus
// its incoming + outgoing edges). Fires only when kind + name are
// both set so the hook can be called unconditionally from the page.
export function useResource(p: ResourceParams) {
  const enabled = Boolean(p.kind && p.name);
  return useQuery<ResourceDetailResponse>({
    queryKey: ['resource', p],
    queryFn: ({ signal }) => fetchJSON<ResourceDetailResponse>(resourceURL(p), { signal }),
    enabled,
  });
}

// NetworkPolicy detail hooks (F-109 / P3-T1). Both fire only for an
// actual NetworkPolicy resource — ResourcePage passes enabled=false
// for every other kind so the hooks can be called unconditionally.

function networkPolicyURL(namespace: string, name: string, sub: string): string {
  return `${apiBase}/networkpolicy/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/${sub}`;
}

// useNetworkPolicySelected wraps GET /networkpolicy/{ns}/{name}/selected.
export function useNetworkPolicySelected(namespace: string, name: string, enabled: boolean) {
  return useQuery<NetworkPolicySelectedResponse>({
    queryKey: ['networkpolicy-selected', namespace, name],
    queryFn: ({ signal }) =>
      fetchJSON<NetworkPolicySelectedResponse>(networkPolicyURL(namespace, name, 'selected'), { signal }),
    enabled: enabled && Boolean(namespace && name),
  });
}

// useNetworkPolicyAllowGraph wraps GET /networkpolicy/{ns}/{name}/allow-graph.
export function useNetworkPolicyAllowGraph(namespace: string, name: string, enabled: boolean) {
  return useQuery<NetworkPolicyAllowGraphResponse>({
    queryKey: ['networkpolicy-allow-graph', namespace, name],
    queryFn: ({ signal }) =>
      fetchJSON<NetworkPolicyAllowGraphResponse>(networkPolicyURL(namespace, name, 'allow-graph'), { signal }),
    enabled: enabled && Boolean(namespace && name),
  });
}

// Params for GET /api/v1alpha1/search.
export interface SearchParams {
  q: string;
  limit?: number;
}

function searchURL(p: SearchParams): string {
  const q = new URLSearchParams({ q: p.q });
  if (p.limit) q.set('limit', String(p.limit));
  return `${apiBase}/search?${q.toString()}`;
}

// useSearch wraps GET /search. Fires only when q is non-empty so the
// command palette can render before the operator types anything. The
// query is debounced at the call site (CommandPalette) so we don't
// throttle here — server-side it's a single GIN match on Tier 2.
export function useSearch(p: SearchParams) {
  const q = p.q.trim();
  return useQuery<SearchResponse>({
    queryKey: ['search', q, p.limit ?? 20],
    queryFn: ({ signal }) =>
      fetchJSON<SearchResponse>(searchURL({ q, limit: p.limit ?? 20 }), { signal }),
    enabled: q.length > 0,
    staleTime: 30_000,
  });
}

function isScopeComplete(p: GraphParams): boolean {
  switch (p.level) {
    case 'cluster':
      return true;
    case 'namespace':
      return Boolean(p.namespace);
    case 'workload':
    case 'resource':
      return Boolean(p.namespace && p.kind && p.name);
  }
}

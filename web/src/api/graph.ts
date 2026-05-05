import { useQuery } from '@tanstack/react-query';

import { fetchJSON } from './client';
import type { Level, ResourceDetailResponse, View } from './types';

// Query params accepted by GET /api/v1alpha1/graph. Only `level` is
// always required; the rest depend on which level is being requested.
export interface GraphParams {
  level: Level;
  namespace?: string;
  kind?: string;
  name?: string;
}

const apiBase = '/api/v1alpha1';

function graphURL(p: GraphParams): string {
  const q = new URLSearchParams({ level: p.level });
  if (p.namespace) q.set('namespace', p.namespace);
  if (p.kind) q.set('kind', p.kind);
  if (p.name) q.set('name', p.name);
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

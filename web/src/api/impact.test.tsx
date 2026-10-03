import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';

import { ApiError } from './client';
import { fetchImpactCapture } from './impactCapture';
import { impactEdgeKey, impactErrorMessage, impactHighlight, impactURL, parseImpactTarget, useImpact } from './impact';
import { impactFixture } from '../test/impactFixture';
import type { ImpactResponse } from './impactTypes';

jest.mock('./impactCapture', () => ({ ...jest.requireActual('./impactCapture'), fetchImpactCapture: jest.fn() }));
const fetchMock = jest.mocked(fetchImpactCapture);
function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
beforeEach(() => fetchMock.mockReset());

test('exact cluster, cluster-scoped namespace, colon names, UID and bounds are encoded without canvas filters', () => {
  const target = parseImpactTarget('east:_/ClusterRole/system:reader', 'uid:a&b')!;
  expect(target.clusterId).toBe('east');
  expect(target.namespace).toBe('');
  expect(target.id).toBe('east:/ClusterRole/system:reader');
  expect(parseImpactTarget('/Namespace/demo')?.id).toBe('/Namespace/demo');
  const url = new URL(impactURL(target, 'dependencies', 10), 'http://localhost');
  expect(url.pathname).toBe('/api/v1/impact/_/ClusterRole/system%3Areader');
  expect(Object.fromEntries(url.searchParams)).toEqual({ cluster: 'east', relation: 'dependencies', max_depth: '10', limit: '200', expected_uid: 'uid:a&b' });
  for (const id of ['namespace', 'demo/Pod', 'demo/Pod/x/y', ':demo/Pod/x', 'a,b:demo/Pod/x', 'demo/Pod/a b']) expect(parseImpactTarget(id)).toBeNull();
});

test('no query until enabled; matching requests share one fetch and cancellation signal', async () => {
  fetchMock.mockResolvedValue(impactFixture());
  const { result, rerender } = renderHook(({ enabled }) => {
    const target = enabled ? parseImpactTarget('demo/ConfigMap/settings', 'fixture-uid') : null;
    return [useImpact(target, 'dependents', 5), useImpact(target, 'dependents', 5)];
  }, { initialProps: { enabled: false }, wrapper: wrapper() });
  expect(fetchMock).not.toHaveBeenCalled();
  rerender({ enabled: true });
  await waitFor(() => expect(result.current[0].isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock.mock.calls[0][1]?.signal).toBeInstanceOf(AbortSignal);
});

test('late old response cannot replace a different-cluster request; abandoned signal is cancelled', async () => {
  let finish!: (result: ImpactResponse) => void;
  fetchMock.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve as typeof finish; }));
  const { result, rerender } = renderHook(({ id }) => useImpact(parseImpactTarget(id), 'dependents', 5), { initialProps: { id: 'demo/ConfigMap/settings' }, wrapper: wrapper() });
  const oldSignal = fetchMock.mock.calls[0][1]?.signal;
  const east = impactFixture('east');
  fetchMock.mockResolvedValueOnce(east);
  rerender({ id: east.analysis.root.id });
  expect(result.current.data).toBeUndefined();
  expect(oldSignal?.aborted).toBe(true);
  await waitFor(() => expect(result.current.data?.scope.clusterId).toBe('east'));
  await act(async () => finish(impactFixture()));
  expect(result.current.data?.scope.clusterId).toBe('east');
});

test.each(['cluster', 'root', 'uid', 'direction', 'depth', 'schema'])('rejects a mismatched %s response', async (field) => {
  const response = impactFixture();
  if (field === 'cluster') response.scope.clusterId = 'hidden';
  if (field === 'root') response.analysis.root.id = 'demo/Pod/other';
  if (field === 'uid') response.analysis.root.uid = 'replacement';
  if (field === 'direction') response.analysis.relation = 'dependencies';
  if (field === 'depth') response.analysis.maxDepth = 1;
  if (field === 'schema') response.schemaVersion = '2';
  fetchMock.mockResolvedValue(response);
  const { result } = renderHook(() => useImpact(parseImpactTarget('demo/ConfigMap/settings', 'fixture-uid'), 'dependents', 5), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data).toBeUndefined();
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

test.each([401, 403, 404, 409, 413, 429, 503, 504, 500])('does not retry or expose raw error text for HTTP %s', async (status) => {
  fetchMock.mockRejectedValue(new ApiError(status, 'test', 'PRIVATE_UPSTREAM_ERROR'));
  const { result } = renderHook(() => useImpact(parseImpactTarget('demo/ConfigMap/settings'), 'dependents', 5), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(impactErrorMessage(result.current.error)).not.toContain('PRIVATE_UPSTREAM_ERROR');
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

test('highlight uses exact returned path tuples only, excludes inferred and runtime edges', () => {
  const highlight = impactHighlight(impactFixture());
  expect([...highlight.nodes]).toEqual(['demo/ConfigMap/settings', 'demo/Pod/api', 'demo/Service/api']);
  expect(highlight.edges.size).toBe(2);
  expect(highlight.edges.has(impactEdgeKey('demo/Service/api', 'demo/Pod/api', 'SELECTS'))).toBe(true);
  expect(highlight.edges.has(impactEdgeKey('demo/Service/api', 'demo/Pod/api', 'CALLS_AT_RUNTIME'))).toBe(false);
  expect(impactHighlight().nodes.size).toBe(0);
});

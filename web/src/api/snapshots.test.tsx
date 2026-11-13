import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';

import { ApiError, fetchJSON } from './client';
import { resolveHistoryScope, useSnapshotDiff, useSnapshots } from './snapshots';
import type { DiffResult } from './types';

jest.mock('./client', () => ({ ...jest.requireActual('./client'), fetchJSON: jest.fn() }));
const diff = (name: string): DiffResult => ({ from: '2026-10-20T00:00:00Z', to: '2026-10-20T01:00:00Z', added: [{ namespace: '', kind: 'Node', name, eventType: 'add', ts: '2026-10-20T00:30:00Z' }], modified: [], removed: [] });
function harness() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> };
}
beforeEach(() => jest.mocked(fetchJSON).mockReset());

test('scope is explicit; missing/failed discovery, all-members and detached selections never become standalone', () => {
  expect(resolveHistoryScope(undefined, null, false).clusterId).toBeNull();
  expect(resolveHistoryScope({ clusters: [] }, null, false).clusterId).toBeNull();
  expect(resolveHistoryScope({ mode: 'single', clusters: [] }, null, true).clusterId).toBeNull();
  expect(resolveHistoryScope({ mode: 'federated', clusters: [] }, null, false).clusterId).toBeNull();
  expect(resolveHistoryScope({ mode: 'federated', clusters: ['east', 'west'] }, null, false).clusterId).toBeNull();
  expect(resolveHistoryScope({ mode: 'federated', clusters: ['east'] }, 'west', false).clusterId).toBeNull();
  expect(resolveHistoryScope({ mode: 'single', clusters: [] }, 'local', false).clusterId).toBe('');
  expect(resolveHistoryScope({ clusters: ['east'] }, 'east', false).clusterId).toBe('east');
});

test('blocked scope sends no history GET; standalone omits selector and member reads encode it once', async () => {
  const { wrapper, client } = harness();
  jest.mocked(fetchJSON).mockResolvedValue({ snapshots: [], count: 0 });
  const hook = renderHook(({ cluster }: { cluster: string | null }) => useSnapshots(cluster), { wrapper, initialProps: { cluster: null as string | null } });
  expect(fetchJSON).not.toHaveBeenCalled();
  hook.rerender({ cluster: '' });
  await waitFor(() => expect(hook.result.current.isSuccess).toBe(true));
  expect(jest.mocked(fetchJSON).mock.calls[0][0]).toBe('/api/v1/snapshots');
  hook.rerender({ cluster: 'east%prod' });
  await waitFor(() => expect(fetchJSON).toHaveBeenCalledTimes(2));
  const url = new URL(jest.mocked(fetchJSON).mock.calls[1][0], 'http://localhost');
  expect(url.searchParams.getAll('cluster')).toEqual(['east%prod']);
  hook.unmount(); client.clear();
});

test('late east response cannot replace west data or a blocked all-members selection', async () => {
  const { wrapper, client } = harness();
  let east!: (value: DiffResult) => void;
  jest.mocked(fetchJSON).mockImplementationOnce(() => new Promise((resolve) => { east = resolve as typeof east; })).mockResolvedValue(diff('west-only'));
  const hook = renderHook(({ cluster }: { cluster: string | null }) => useSnapshotDiff('1h', 'now', '', cluster), { wrapper, initialProps: { cluster: 'east' as string | null } });
  await waitFor(() => expect(fetchJSON).toHaveBeenCalledTimes(1));
  hook.rerender({ cluster: 'west' });
  await waitFor(() => expect(hook.result.current.data?.added[0].name).toBe('west-only'));
  await act(async () => { east(diff('east-private')); });
  expect(hook.result.current.data?.added[0].name).toBe('west-only');
  hook.rerender({ cluster: null });
  expect(hook.result.current.data).toBeUndefined();
  expect(fetchJSON).toHaveBeenCalledTimes(2);
  hook.unmount(); client.clear();
});

test.each([401, 403, 413, 429, 503, 504])('HTTP %i is surfaced with no retry or unscoped fallback', async (status) => {
  const { wrapper, client } = harness();
  jest.mocked(fetchJSON).mockRejectedValue(new ApiError(status, 'unavailable', 'closed failure'));
  const hook = renderHook(() => useSnapshotDiff('1h', 'now', 'demo', 'east'), { wrapper });
  await waitFor(() => expect(hook.result.current.isError).toBe(true));
  expect(fetchJSON).toHaveBeenCalledTimes(1);
  const url = new URL(jest.mocked(fetchJSON).mock.calls[0][0], 'http://localhost');
  expect(url.searchParams.get('cluster')).toBe('east');
  expect(url.searchParams.get('namespace')).toBe('demo');
  hook.unmount(); client.clear();
});

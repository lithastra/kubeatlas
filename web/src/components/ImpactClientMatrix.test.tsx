import { useState } from 'react';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

import matrix from '../../../test/fixtures/impact-analysis/client-matrix.json';
import captures from '../../../test/fixtures/impact-analysis/client-captures.json';
import { useImpact, parseImpactTarget } from '../api/impact';
import type { ImpactRelation, ImpactResponse } from '../api/impactTypes';
import { BlastRadiusPanel } from './BlastRadiusPanel';

// Only transport and the browser's download sink are substituted. The actual
// reader, complete schema/identity checks, query hook, panel and exporters run.
// pkg/api locks these same captures to real handler/store/engine results, while
// cmd/kubectl-atlas tests the command against them in all three output formats.
function MatrixFlow({ entry }: { entry: typeof matrix.cases[number] }) {
  const [selected, setSelected] = useState(false);
  const id = `${entry.cluster ? `${entry.cluster}:` : ''}${entry.namespace}/${entry.kind}/${entry.rootName}`;
  const query = useImpact(selected ? parseImpactTarget(id, entry.uid) : null, entry.relation as ImpactRelation, entry.depth);
  return <><button onClick={() => setSelected(true)}>Analyze selected resource</button>
    <BlastRadiusPanel response={query.data} loading={query.isFetching} error={query.error} onRefresh={() => { void query.refetch(); }} />
  </>;
}

const originalFetch = globalThis.fetch;
const originalCreate = URL.createObjectURL;
const originalRevoke = URL.revokeObjectURL;
afterEach(() => { globalThis.fetch = originalFetch; URL.createObjectURL = originalCreate; URL.revokeObjectURL = originalRevoke; jest.restoreAllMocks(); });

test.each(matrix.cases)('$name: selected query -> displayed evidence -> exact JSON and HTML capture', async (entry) => {
  const raw = JSON.stringify(captures[entry.name as keyof typeof captures]);
  const bytes = new TextEncoder().encode(raw);
  globalThis.fetch = jest.fn(async () => ({status: 200, headers: new Map([['content-type','application/json']]),
    body: new ReadableStream({ start(c) { c.enqueue(bytes); c.close(); } }),
  } as unknown as Response));
  const blobs: Blob[] = [];
  URL.createObjectURL = jest.fn((blob: Blob) => { blobs.push(blob); return 'blob:synthetic-download'; });
  URL.revokeObjectURL = jest.fn();
  const click = jest.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
  const client = new QueryClient({defaultOptions: {queries: {retry: false}}});
  const view = render(<QueryClientProvider client={client}><MatrixFlow entry={entry} /></QueryClientProvider>);
  expect(globalThis.fetch).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole('button', {name: 'Analyze selected resource'}));
  expect(await screen.findByText(`Cluster: ${entry.cluster || 'standalone'} · All namespaces`)).toBeVisible();
  const prefix = entry.truncated ? 'At least ' : '';
  expect(screen.getByText(`${prefix}${entry.ordinary.length} observed · ${entry.direct} direct · ${entry.ordinary.length-entry.direct} indirect`)).toBeVisible();
  if (entry.truncated) expect(screen.getByText('Truncated: max_depth. Counts are lower bounds.')).toBeVisible();
  if (entry.ordinary.length === 0) expect(screen.getByText(`No ${entry.relation} observed; analysis incomplete.`)).toBeVisible();
  fireEvent.click(screen.getByText('Authorization associations (separate)'));
  if (entry.authorization.length) expect(screen.getByText('3 observed · 1 direct · 2 indirect')).toBeInTheDocument();
  expect(screen.getByText('Not effective permissions, credential use, or ordinary impact counts.')).toBeInTheDocument();
  const evidence = (JSON.parse(raw) as ImpactResponse).analysis;
  for (const match of [...evidence.resources, ...evidence.authorization.resources]) {
    const resource = match.resource;
    const label = `${resource.kind}/${resource.name} · ${resource.namespace || 'cluster-scoped'} · ${match.depth === 1 ? 'Direct' : `Indirect (${match.depth} hops)`}${resource.referenceOnly ? ' · reference only' : ''}`;
    const summary = screen.getByText(label, { exact: true });
    fireEvent.click(summary);
    const details = summary.closest('details');
    expect(details).not.toBeNull();
    for (const step of match.path) {
      expect(within(details!).getByText(`${step.traversalFrom} → ${step.traversalTo}`, { exact: true })).toBeVisible();
      expect(within(details!).getByText(`${step.type} · Stored edge: ${step.from} → ${step.to}`, { exact: false })).toBeVisible();
    }
  }
  expect(screen.getByRole('button', {name:'Download JSON'})).toBeDisabled();
  fireEvent.click(screen.getByRole('checkbox'));
  fireEvent.click(screen.getByRole('button',{name:'Download JSON'}));
  fireEvent.click(screen.getByRole('button',{name:'Download HTML'}));
  expect(click).toHaveBeenCalledTimes(2);
  const text = (blob: Blob) => new Promise<string>((resolve,reject) => {const reader = new FileReader();reader.onload=()=>resolve(String(reader.result));reader.onerror=reject;reader.readAsText(blob);});
  expect(await text(blobs[0])).toBe(raw);
  const html = new DOMParser().parseFromString(await text(blobs[1]),'text/html');
  expect(html.getElementById('captured-json')?.textContent).toBe(raw);
  expect(html.querySelectorAll('script,iframe,object,embed,link,img')).toHaveLength(0);
  expect(html.body.textContent).not.toContain('west:');
  const [url] = jest.mocked(globalThis.fetch).mock.calls[0];
  const parsed = new URL(String(url),'http://localhost');
  expect(parsed.pathname).toBe(`/api/v1/impact/${entry.namespace}/${entry.kind}/${entry.rootName}`);
  expect(parsed.searchParams.get('cluster') || '').toBe(entry.cluster);
  expect(parsed.searchParams.get('relation')).toBe(entry.relation);
  expect(parsed.searchParams.get('max_depth')).toBe(String(entry.depth));
  expect(parsed.searchParams.get('expected_uid')).toBe(entry.uid);
  expect(parsed.searchParams.get('limit')).toBe('200');
  expect(globalThis.fetch).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole('button',{name:'Refresh analysis'}));
  await waitFor(() => expect(screen.getByRole('checkbox')).not.toBeChecked());
  expect(screen.getByRole('button',{name:'Download JSON'})).toBeDisabled();
  view.unmount(); client.clear();
});

import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

import { fetchImpactCapture, parseImpactCapture } from '../api/impactCapture';
import { parseImpactTarget } from '../api/impact';
import { impactFixture } from '../test/impactFixture';
import { ImpactExplorer } from './ImpactExplorer';

jest.mock('../api/impactCapture', () => ({ ...jest.requireActual('../api/impactCapture'), fetchImpactCapture: jest.fn() }));
const fetchMock = jest.mocked(fetchImpactCapture);

test('resource -> explicit analysis -> dependencies -> bounded depth -> close, without local graph requests', async () => {
  fetchMock.mockReset();
  fetchMock.mockImplementation(async (url) => {
    const query = new URL(url, 'http://localhost').searchParams;
    const response = impactFixture();
    response.analysis.relation = query.get('relation') as 'dependents' | 'dependencies';
    response.analysis.maxDepth = Number(query.get('max_depth'));
    if (response.analysis.relation === 'dependencies') {
      response.analysis.resources = [];
      Object.assign(response.analysis.counts, { total: 0, direct: 0, indirect: 0, workloads: 0, pods: 0, other: 0, lowerBound: true });
      response.analysis.referenceEvidence = [];
    }
    Object.assign(response.analysis.authorization, { maxDepth: response.analysis.maxDepth });
    return parseImpactCapture(JSON.stringify(response));
  });
  const client = new QueryClient();
  render(<QueryClientProvider client={client}><ImpactExplorer target={parseImpactTarget('demo/ConfigMap/settings', 'fixture-uid')!} /></QueryClientProvider>);
  expect(fetchMock).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole('button', { name: 'Analyze impact' }));
  expect(await screen.findByText(/At least 2 observed/)).toBeVisible();
  fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Relation' }));
  fireEvent.click(screen.getByRole('option', { name: 'Dependencies — resources this depends on' }));
  expect(await screen.findByText('No dependencies observed; analysis incomplete.')).toBeVisible();
  fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Maximum depth' }));
  fireEvent.click(screen.getByRole('option', { name: '10' }));
  await waitFor(() => expect(screen.getByText('dependencies · Depth 10 · Limit 200')).toBeVisible());
  expect(fetchMock).toHaveBeenCalledTimes(3);
  for (const [url] of fetchMock.mock.calls) {
    expect(url).toContain('/api/v1/impact/demo/ConfigMap/settings?');
    expect(url).toContain('expected_uid=fixture-uid');
  }
  fireEvent.click(screen.getByRole('button', { name: 'Close impact analysis' }));
  expect(screen.queryByRole('heading', { name: 'Impact analysis' })).not.toBeInTheDocument();
});

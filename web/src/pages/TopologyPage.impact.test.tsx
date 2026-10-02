import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Provider } from 'react-redux';
import { I18nextProvider } from 'react-i18next';
import { MemoryRouter } from 'react-router-dom';

import { fetchJSON } from '../api/client';
import { fetchImpactCapture, parseImpactCapture } from '../api/impactCapture';
import type { ImpactResponse } from '../api/impactTypes';
import type { View } from '../api/types';
import { AtlasThemeProvider } from '../theme';
import { AtlasShell, useBlastRadius, useClusterSelection, useRightPanel } from '../shell';
import { store } from '../store';
import i18n from '../i18n';
import { impactFixture } from '../test/impactFixture';
import { TopologyPage } from './TopologyPage';
import captures from '../../../test/fixtures/impact-analysis/client-captures.json';

jest.mock('../api/client', () => ({ ...jest.requireActual('../api/client'), fetchJSON: jest.fn() }));
jest.mock('../api/impactCapture', () => ({ ...jest.requireActual('../api/impactCapture'), fetchImpactCapture: jest.fn() }));
jest.mock('../components/TopologyView', () => ({
  TopologyView: ({ view, impactResponse, onSelect }: { view?: View; impactResponse?: ImpactResponse; onSelect: (id: string) => void }) => <div>
    {view?.nodes.map((node) => <button key={node.id} onClick={() => onSelect(node.id)}>{node.id}</button>)}
    <output data-testid="highlight-response">{impactResponse?.scope.clusterId || (impactResponse ? 'standalone' : 'none')}</output>
  </div>,
}));

function RightPanel() {
  const { content } = useRightPanel();
  const { setSelected } = useClusterSelection();
  const { exit } = useBlastRadius();
  return <><button onClick={() => setSelected('east')}>Switch east</button><button onClick={() => setSelected('west')}>Switch west</button><button onClick={exit}>Exit analysis</button>{content}</>;
}

test('concrete resource selection uses the API and cluster changes clear the result and highlights', async () => {
  const fetchMock = jest.mocked(fetchJSON);
  const impactMock = jest.mocked(fetchImpactCapture);
  impactMock.mockReset();
  impactMock.mockImplementation(async (url) => parseImpactCapture(JSON.stringify(impactFixture(new URL(url, 'http://localhost').searchParams.get('cluster') || ''))));
  fetchMock.mockReset();
  fetchMock.mockImplementation(async (url) => {
    const parsed = new URL(url, 'http://localhost');
    const cluster = parsed.searchParams.get('cluster') || '';
    if (parsed.pathname.includes('/graph')) return {
      level: 'resource', clusters: cluster ? [cluster] : [], edges: [],
      nodes: [
        { id: 'demo', type: 'aggregated', name: 'demo', kind: 'Namespace' },
        { id: `${cluster ? `${cluster}:` : ''}demo/ConfigMap/settings`, type: 'resource', namespace: 'demo', kind: 'ConfigMap', name: 'settings' },
      ],
    };
    if (parsed.pathname.includes('/resources/')) return { resource: { uid: 'fixture-uid', kind: 'ConfigMap', namespace: 'demo', name: 'settings' }, incoming: [], outgoing: [] };
    return { keys: [], values: [], clusters: [] };
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<Provider store={store}><QueryClientProvider client={client}><I18nextProvider i18n={i18n}><AtlasThemeProvider><TopologyPage /><RightPanel /></AtlasThemeProvider></I18nextProvider></QueryClientProvider></Provider>);
  fireEvent.click(await screen.findByRole('button', { name: 'demo' }));
  expect(screen.queryByRole('button', { name: /Analyze impact/ })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'demo/ConfigMap/settings' }));
  fireEvent.click(screen.getByRole('button', { name: /Analyze impact/ }));
  expect(await screen.findByText(/At least 2 observed/)).toBeVisible();
  expect(screen.getByTestId('highlight-response')).toHaveTextContent('standalone');
  fireEvent.click(screen.getByRole('button', { name: 'Switch east' }));
  await waitFor(() => expect(screen.queryByText(/At least 2 observed/)).not.toBeInTheDocument());
  expect(await screen.findByTestId('highlight-response')).toHaveTextContent('none');
  fireEvent.click(await screen.findByRole('button', { name: 'east:demo/ConfigMap/settings' }));
  fireEvent.click(screen.getByRole('button', { name: /Analyze impact/ }));
  expect(await screen.findByText('Cluster: east · All namespaces')).toBeVisible();
  expect(screen.getByTestId('highlight-response')).toHaveTextContent('east');
  fireEvent.click(screen.getByRole('button', { name: 'Exit analysis' }));
  await waitFor(() => expect(screen.queryByRole('heading', { name: 'Impact analysis' })).not.toBeInTheDocument());
  expect(screen.getByRole('button', { name: /Analyze impact/ })).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: 'Switch west' }));
  await waitFor(() => expect(screen.queryByText('Cluster: east · All namespaces')).not.toBeInTheDocument());
  const requests = impactMock.mock.calls;
  expect(requests).toHaveLength(2);
  expect(requests[1][0]).toContain('cluster=east');
  expect(await screen.findByTestId('highlight-response')).toHaveTextContent('none');
});

test.each([true, false])('shell keeps analysis controls with the result and restores resource navigation (narrow=%s)', async (narrow) => {
  const original = window.matchMedia;
  window.matchMedia = jest.fn((query: string) => ({
    matches: query.includes('max-width') && narrow, media: query, onchange: null,
    addListener: jest.fn(), removeListener: jest.fn(), addEventListener: jest.fn(),
    removeEventListener: jest.fn(), dispatchEvent: jest.fn(),
  }));
  const fetchMock = jest.mocked(fetchJSON);
  fetchMock.mockReset();
  fetchMock.mockImplementation(async (url) => {
    if (url.includes('/graph')) return { level: 'resource', edges: [], nodes: [
      { id: 'demo/ConfigMap/settings', type: 'resource', namespace: 'demo', kind: 'ConfigMap', name: 'settings' },
    ] };
    if (url.includes('/resources/')) return { resource: {uid: 'matrix-config'}, incoming: [], outgoing: [] };
    return {clusters: [], labels: []};
  });
  const impactMock = jest.mocked(fetchImpactCapture);
  impactMock.mockReset();
  impactMock.mockImplementation(async (url) => {
    const q = new URL(url, 'http://localhost').searchParams;
    const entry = q.get('relation') === 'dependencies' ? captures['incomplete-empty'] :
      q.get('max_depth') === '1' ? captures.truncated : captures.ordinary;
    return parseImpactCapture(JSON.stringify(entry));
  });
  const client = new QueryClient({defaultOptions: {queries: {retry: false}}});
  const view = render(<Provider store={store}><QueryClientProvider client={client}><I18nextProvider i18n={i18n}><MemoryRouter><AtlasThemeProvider><AtlasShell><TopologyPage /></AtlasShell></AtlasThemeProvider></MemoryRouter></I18nextProvider></QueryClientProvider></Provider>);
  try {
    const root = await screen.findByRole('button', {name: 'demo/ConfigMap/settings'});
    root.focus();
    fireEvent.click(root);
    const close = await screen.findByRole('button', {name: 'Close detail panel'});
    if (narrow) {
      expect(close).toHaveFocus();
      expect(root.closest('[inert]')).not.toBeNull();
    } else {
      expect(root).toHaveFocus();
      expect(root.closest('[inert]')).toBeNull();
    }
    fireEvent.click(await screen.findByRole('button', {name: /Analyze impact/}));
    const panel = screen.getByRole('region', {name: 'Detail panel'});
    expect(await within(panel).findByText('2 observed · 1 direct · 1 indirect')).toBeVisible();
    const controls = within(panel).getByRole('region', {name: 'Impact analysis controls'});
    expect(screen.getAllByRole('region', {name: 'Impact analysis controls'})).toHaveLength(1);
    expect(impactMock).toHaveBeenCalledTimes(1);
    fireEvent.click(within(panel).getByRole('checkbox'));
    fireEvent.click(within(controls).getByRole('button', {name: '1'}));
    expect(await within(panel).findByText('At least 1 observed · 1 direct · 0 indirect')).toBeVisible();
    expect(within(panel).getByRole('checkbox')).not.toBeChecked();
    expect(within(panel).getByRole('button', {name: 'Download JSON'})).toBeDisabled();
    fireEvent.click(within(controls).getByRole('button', {name: '5'}));
    await within(panel).findByText('2 observed · 1 direct · 1 indirect');
    fireEvent.click(within(controls).getByRole('button', {name: 'Dependencies'}));
    expect(await within(panel).findByText('No dependencies observed; analysis incomplete.')).toBeVisible();
    fireEvent.keyDown(within(controls).getByRole('button', {name: 'Dependencies'}), {key: 'Escape'});
    expect(await within(panel).findByRole('button', {name: /Analyze impact/})).toBeVisible();
    expect(screen.queryByRole('region', {name: 'Impact analysis controls'})).not.toBeInTheDocument();
    if (narrow) expect(within(panel).getByRole('button', {name: 'Close detail panel'})).toHaveFocus();
    fireEvent.click(within(panel).getByRole('button', {name: /Analyze impact/}));
    await within(panel).findByText('2 observed · 1 direct · 1 indirect');
    fireEvent.click(within(panel).getByRole('button', {name: 'Exit impact analysis'}));
    await within(panel).findByRole('button', {name: /Analyze impact/});
    fireEvent.click(within(panel).getByRole('button', {name: 'Close detail panel'}));
    await waitFor(() => expect(screen.queryByRole('region', {name: 'Detail panel'})).not.toBeInTheDocument());
    expect(root.closest('[inert]')).toBeNull();
    if (narrow) expect(root).toHaveFocus();
    fireEvent.click(root);
    expect(await screen.findByRole('region', {name: 'Detail panel'})).toBeVisible();
    expect(screen.getByRole('button', {name: /Analyze impact/})).toBeVisible();
  } finally {
    view.unmount(); client.clear(); window.matchMedia = original;
  }
});

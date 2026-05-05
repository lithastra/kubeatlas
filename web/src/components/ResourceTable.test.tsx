import { render, screen } from '@testing-library/react';
import { Provider as ReduxProvider } from 'react-redux';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { I18nextProvider } from 'react-i18next';
import { MemoryRouter } from 'react-router-dom';
import { configureStore } from '@reduxjs/toolkit';

import { ResourceTable } from './ResourceTable';
import { filterSlice } from '../store/filterSlice';
import i18n from '../i18n';
import * as graphAPI from '../api/graph';
import type { View } from '../api/types';

function buildStore(initial: { namespace: string | null }) {
  return configureStore({
    reducer: { filter: filterSlice.reducer },
    preloadedState: {
      filter: { namespace: initial.namespace, kind: null, search: '' },
    },
  });
}

function renderWithProviders(node: React.ReactNode, namespace: string | null) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <ReduxProvider store={buildStore({ namespace })}>
      <QueryClientProvider client={queryClient}>
        <I18nextProvider i18n={i18n}>
          <MemoryRouter>{node}</MemoryRouter>
        </I18nextProvider>
      </QueryClientProvider>
    </ReduxProvider>
  );
}

describe('ResourceTable', () => {
  it('shows the empty-namespace alert when no namespace is picked', () => {
    renderWithProviders(<ResourceTable />, null);
    expect(screen.getByText(/all namespaces/i)).toBeInTheDocument();
  });

  it('renders rows from the namespace-level view', async () => {
    const fixture: View = {
      level: 'namespace',
      nodes: [
        {
          id: 'demo/Deployment/api',
          type: 'aggregated',
          kind: 'Deployment',
          namespace: 'demo',
          name: 'api',
          edge_count_in: 0,
          edge_count_out: 0,
        },
        {
          id: 'demo/ConfigMap/app-config',
          type: 'resource',
          kind: 'ConfigMap',
          namespace: 'demo',
          name: 'app-config',
          edge_count_in: 0,
          edge_count_out: 0,
        },
      ],
      edges: [],
    };
    jest.spyOn(graphAPI, 'useNamespaceGraph').mockReturnValue({
      data: fixture,
      isLoading: false,
      isError: false,
      error: null,
      // Cast satisfies the rest of UseQueryResult; we only consume
      // the four fields above.
    } as unknown as ReturnType<typeof graphAPI.useNamespaceGraph>);

    renderWithProviders(<ResourceTable />, 'demo');
    expect(await screen.findByText('api')).toBeInTheDocument();
    expect(screen.getByText('app-config')).toBeInTheDocument();
    expect(screen.getAllByText('Deployment').length).toBeGreaterThan(0);
  });

  it('shows the empty-list alert when the namespace has no nodes', () => {
    jest.spyOn(graphAPI, 'useNamespaceGraph').mockReturnValue({
      data: { level: 'namespace', nodes: [], edges: [] } as View,
      isLoading: false,
      isError: false,
      error: null,
    } as unknown as ReturnType<typeof graphAPI.useNamespaceGraph>);

    renderWithProviders(<ResourceTable />, 'demo');
    expect(screen.getByText(/no resources match/i)).toBeInTheDocument();
  });

  it('surfaces fetch errors', () => {
    jest.spyOn(graphAPI, 'useNamespaceGraph').mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new Error('boom'),
    } as unknown as ReturnType<typeof graphAPI.useNamespaceGraph>);

    renderWithProviders(<ResourceTable />, 'demo');
    expect(screen.getByText('boom')).toBeInTheDocument();
  });
});

import { render, screen, fireEvent } from '@testing-library/react';
import { Provider as ReduxProvider } from 'react-redux';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { I18nextProvider } from 'react-i18next';
import { configureStore } from '@reduxjs/toolkit';

import { NamespacePicker } from './NamespacePicker';
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
  const store = buildStore({ namespace });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return {
    store,
    ...render(
      <ReduxProvider store={store}>
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>{node}</I18nextProvider>
        </QueryClientProvider>
      </ReduxProvider>
    ),
  };
}

describe('NamespacePicker', () => {
  it('lists namespaces from the cluster-level view, filtering the _cluster bucket', () => {
    const fixture: View = {
      level: 'cluster',
      nodes: [
        {
          id: '_cluster',
          type: 'aggregated',
          edge_count_in: 0,
          edge_count_out: 0,
        },
        { id: 'kube-system', type: 'aggregated', edge_count_in: 0, edge_count_out: 0 },
        { id: 'petclinic', type: 'aggregated', edge_count_in: 0, edge_count_out: 0 },
      ],
      edges: [],
    };
    jest.spyOn(graphAPI, 'useClusterGraph').mockReturnValue({
      data: fixture,
      isLoading: false,
      isError: false,
      error: null,
    } as unknown as ReturnType<typeof graphAPI.useClusterGraph>);

    renderWithProviders(<NamespacePicker />, null);
    const input = screen.getByRole('combobox');
    fireEvent.mouseDown(input);
    const options = screen.getAllByRole('option');
    const optionText = options.map((el) => el.textContent);
    expect(optionText).toEqual(['kube-system', 'petclinic']);
    expect(optionText).not.toContain('_cluster');
  });

  it('dispatches setNamespace when a namespace is chosen', () => {
    jest.spyOn(graphAPI, 'useClusterGraph').mockReturnValue({
      data: { level: 'cluster', nodes: [{ id: 'demo', type: 'aggregated', edge_count_in: 0, edge_count_out: 0 }], edges: [] } as View,
      isLoading: false,
      isError: false,
      error: null,
    } as unknown as ReturnType<typeof graphAPI.useClusterGraph>);

    const { store } = renderWithProviders(<NamespacePicker />, null);
    const input = screen.getByRole('combobox');
    fireEvent.mouseDown(input);
    fireEvent.click(screen.getByRole('option', { name: 'demo' }));
    expect(store.getState().filter.namespace).toBe('demo');
  });
});

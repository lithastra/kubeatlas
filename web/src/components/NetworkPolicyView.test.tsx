import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { I18nextProvider } from 'react-i18next';
import { MemoryRouter } from 'react-router-dom';

import { NetworkPolicyView } from './NetworkPolicyView';
import i18n from '../i18n';
import * as graphAPI from '../api/graph';
import type { NetworkPolicyAllowGraphResponse, NetworkPolicySelectedResponse, Resource } from '../api/types';

function renderView() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <I18nextProvider i18n={i18n}>
        <MemoryRouter>
          <NetworkPolicyView namespace="petclinic" name="api-allow" />
        </MemoryRouter>
      </I18nextProvider>
    </QueryClientProvider>
  );
}

function res(kind: string, namespace: string, name: string): Resource {
  return { kind, namespace, name };
}

describe('NetworkPolicyView', () => {
  it('renders selected + allow-from + allow-to lists', () => {
    const selected: NetworkPolicySelectedResponse = {
      networkPolicy: res('NetworkPolicy', 'petclinic', 'api-allow'),
      selected: [res('Deployment', 'petclinic', 'api')],
      count: 1,
    };
    const allow: NetworkPolicyAllowGraphResponse = {
      networkPolicy: res('NetworkPolicy', 'petclinic', 'api-allow'),
      allowFrom: [res('StatefulSet', 'petclinic', 'postgres')],
      allowTo: [res('StatefulSet', 'petclinic', 'postgres')],
    };
    jest.spyOn(graphAPI, 'useNetworkPolicySelected').mockReturnValue({
      data: selected,
      isLoading: false,
      error: null,
    } as unknown as ReturnType<typeof graphAPI.useNetworkPolicySelected>);
    jest.spyOn(graphAPI, 'useNetworkPolicyAllowGraph').mockReturnValue({
      data: allow,
      isLoading: false,
      error: null,
    } as unknown as ReturnType<typeof graphAPI.useNetworkPolicyAllowGraph>);

    renderView();
    expect(screen.getByTestId('networkpolicy-view')).toBeInTheDocument();
    expect(screen.getByText('Selected (1)')).toBeInTheDocument();
    expect(screen.getByText('Allow from (1)')).toBeInTheDocument();
    expect(screen.getByText('Allow to (1)')).toBeInTheDocument();
    expect(screen.getByText('Deployment/api')).toBeInTheDocument();
  });

  it('shows zero-count headers when the policy has no edges', () => {
    jest.spyOn(graphAPI, 'useNetworkPolicySelected').mockReturnValue({
      data: { networkPolicy: res('NetworkPolicy', 'petclinic', 'api-allow'), selected: [], count: 0 },
      isLoading: false,
      error: null,
    } as unknown as ReturnType<typeof graphAPI.useNetworkPolicySelected>);
    jest.spyOn(graphAPI, 'useNetworkPolicyAllowGraph').mockReturnValue({
      data: { networkPolicy: res('NetworkPolicy', 'petclinic', 'api-allow'), allowFrom: [], allowTo: [] },
      isLoading: false,
      error: null,
    } as unknown as ReturnType<typeof graphAPI.useNetworkPolicyAllowGraph>);

    renderView();
    expect(screen.getByText('Selected (0)')).toBeInTheDocument();
    expect(screen.getByText('Allow from (0)')).toBeInTheDocument();
    expect(screen.getByText('Allow to (0)')).toBeInTheDocument();
  });

  it('surfaces a fetch error', () => {
    jest.spyOn(graphAPI, 'useNetworkPolicySelected').mockReturnValue({
      data: undefined,
      isLoading: false,
      error: new Error('boom'),
    } as unknown as ReturnType<typeof graphAPI.useNetworkPolicySelected>);
    jest.spyOn(graphAPI, 'useNetworkPolicyAllowGraph').mockReturnValue({
      data: undefined,
      isLoading: false,
      error: null,
    } as unknown as ReturnType<typeof graphAPI.useNetworkPolicyAllowGraph>);

    renderView();
    expect(screen.getByText('boom')).toBeInTheDocument();
  });
});

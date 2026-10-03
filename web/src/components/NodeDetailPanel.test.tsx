import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

import { fetchJSON } from '../api/client';
import { BlastRadiusProvider, useBlastRadius } from '../shell/BlastRadiusContext';
import { NodeDetailPanel } from './NodeDetailPanel';

jest.mock('../api/client', () => ({ ...jest.requireActual('../api/client'), fetchJSON: jest.fn() }));

function Selected() {
  const blast = useBlastRadius();
  return <output>{blast.active ? `${blast.rootId} ${blast.direction} ${blast.depth}` : 'inactive'}</output>;
}

test('aggregated nodes cannot start analysis and federated resources never fetch unscoped details', () => {
  const client = new QueryClient();
  const fetchMock = jest.mocked(fetchJSON);
  fetchMock.mockReset();
  const wrap = (concrete: boolean) => <QueryClientProvider client={client}><BlastRadiusProvider><NodeDetailPanel nodeId="east:demo/ConfigMap/settings" concrete={concrete} /><Selected /></BlastRadiusProvider></QueryClientProvider>;
  const { rerender } = render(wrap(false));
  expect(screen.queryByRole('button', { name: /Analyze impact/ })).not.toBeInTheDocument();
  expect(fetchMock).not.toHaveBeenCalled();
  rerender(wrap(true));
  fireEvent.click(screen.getByRole('button', { name: /Analyze impact/ }));
  expect(screen.getByText('east:demo/ConfigMap/settings dependents 5')).toBeInTheDocument();
  expect(fetchMock).not.toHaveBeenCalled();
});

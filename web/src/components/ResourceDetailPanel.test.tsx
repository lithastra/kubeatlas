import { fireEvent, render, screen } from '@testing-library/react';
import { I18nextProvider } from 'react-i18next';

import type { Resource } from '../api/types';
import i18n from '../i18n';
import { ResourceDetailPanel } from './ResourceDetailPanel';

jest.mock('./NeighborView', () => ({ NeighborView: () => null }));

describe('ResourceDetailPanel', () => {
  it('renders annotated resources with the local disclosure icon', () => {
    const resource: Resource = {
      kind: 'Deployment',
      namespace: 'petclinic',
      name: 'api',
      annotations: { 'example.com/owner': 'platform' },
    };

    const { container } = render(
      <I18nextProvider i18n={i18n}>
        <ResourceDetailPanel resource={resource} incoming={[]} outgoing={[]} />
      </I18nextProvider>,
    );

    expect(screen.getByTestId('resource-detail-header')).toBeInTheDocument();
    expect(screen.getByText('Annotations (1)')).toBeInTheDocument();
    expect(
      container.querySelector('use[href$="#atlas-icon-chevron-down"]'),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByText('Annotations (1)'));
    expect(screen.getByText(/example\.com\/owner:/)).toHaveTextContent(
      'example.com/owner: platform',
    );
  });
});

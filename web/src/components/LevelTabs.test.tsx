import { render, screen, fireEvent } from '@testing-library/react';
import { I18nextProvider } from 'react-i18next';

import { LevelTabs } from './LevelTabs';
import i18n from '../i18n';

describe('LevelTabs', () => {
  it('renders four level tabs labelled from glossary', () => {
    render(
      <I18nextProvider i18n={i18n}>
        <LevelTabs value="cluster" onChange={() => {}} />
      </I18nextProvider>
    );
    for (const label of ['Cluster', 'Namespace', 'Workload', 'Resource']) {
      expect(screen.getByRole('tab', { name: label })).toBeInTheDocument();
    }
  });

  it('emits the selected level on click', () => {
    const onChange = jest.fn();
    render(
      <I18nextProvider i18n={i18n}>
        <LevelTabs value="cluster" onChange={onChange} />
      </I18nextProvider>
    );
    fireEvent.click(screen.getByRole('tab', { name: 'Namespace' }));
    expect(onChange).toHaveBeenCalledWith('namespace');
  });

  it('disables workload + resource tabs when their props are set', () => {
    render(
      <I18nextProvider i18n={i18n}>
        <LevelTabs value="cluster" onChange={() => {}} disableWorkload disableResource />
      </I18nextProvider>
    );
    expect(screen.getByRole('tab', { name: 'Workload' })).toBeDisabled();
    expect(screen.getByRole('tab', { name: 'Resource' })).toBeDisabled();
    expect(screen.getByRole('tab', { name: 'Cluster' })).toBeEnabled();
    expect(screen.getByRole('tab', { name: 'Namespace' })).toBeEnabled();
  });
});

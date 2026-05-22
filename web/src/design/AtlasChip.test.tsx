import { render, screen, fireEvent } from '@testing-library/react';
import { I18nextProvider } from 'react-i18next';

import { AtlasChip } from './AtlasChip';
import i18n from '../i18n';

function r(node: React.ReactNode) {
  return render(<I18nextProvider i18n={i18n}>{node}</I18nextProvider>);
}

describe('AtlasChip', () => {
  it('filter variant exposes aria-checked when selected', () => {
    r(<AtlasChip atlasVariant="filter" label="USES_CONFIGMAP" selected onClick={() => {}} />);
    const chip = screen.getByRole('switch');
    expect(chip).toHaveAttribute('aria-checked', 'true');
  });

  it('cluster variant exposes aria-selected', () => {
    r(<AtlasChip atlasVariant="cluster" label="prod" selected onClick={() => {}} />);
    expect(screen.getByRole('option')).toHaveAttribute('aria-selected', 'true');
  });

  it('tag variant is aria-readonly and not clickable', () => {
    r(<AtlasChip atlasVariant="tag" label="team=payments" />);
    expect(screen.getByText('team=payments').closest('[aria-readonly]')).not.toBeNull();
  });

  it('forwards onClick on clickable variants', () => {
    const onClick = jest.fn();
    r(<AtlasChip atlasVariant="filter" label="USES_SECRET" onClick={onClick} />);
    fireEvent.click(screen.getByRole('switch'));
    expect(onClick).toHaveBeenCalledTimes(1);
  });
});

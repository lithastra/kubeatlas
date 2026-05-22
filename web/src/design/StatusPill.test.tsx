import { render, screen } from '@testing-library/react';
import { I18nextProvider } from 'react-i18next';

import { StatusPill } from './StatusPill';
import i18n from '../i18n';

function r(node: React.ReactNode) {
  return render(<I18nextProvider i18n={i18n}>{node}</I18nextProvider>);
}

describe('StatusPill', () => {
  it('renders the default label per variant', () => {
    r(<StatusPill variant="healthy" />);
    expect(screen.getByLabelText('Healthy')).toBeInTheDocument();
    expect(screen.getByText('Healthy')).toBeInTheDocument();
  });

  it('uses a custom label when provided', () => {
    r(<StatusPill variant="warning" label="Warning · 1/3 pods" />);
    expect(screen.getByLabelText('Warning · 1/3 pods')).toBeInTheDocument();
  });

  it('renders all six variants without throwing', () => {
    const variants = [
      'healthy',
      'warning',
      'error',
      'orphan',
      'deleted',
      'unknown',
    ] as const;
    for (const v of variants) r(<StatusPill variant={v} />);
    // One of each rendered: pick deleted to confirm it has the
    // strikethrough rule (verifying via label still present).
    expect(screen.getByLabelText('Deleted')).toBeInTheDocument();
  });
});

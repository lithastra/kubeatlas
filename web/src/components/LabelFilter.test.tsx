import { render, screen, fireEvent } from '@testing-library/react';
import { I18nextProvider } from 'react-i18next';

import { LabelFilter } from './LabelFilter';
import i18n from '../i18n';
import * as labelsAPI from '../api/labels';
import type { LabelsResponse } from '../api/types';

const fixture: LabelsResponse = {
  count: 2,
  labels: [
    {
      key: 'team',
      resourceCount: 5,
      valueCount: 2,
      values: [
        { value: 'payments', count: 3 },
        { value: 'search', count: 2 },
      ],
    },
    { key: 'env', resourceCount: 4, valueCount: 1, values: [{ value: 'prod', count: 4 }] },
  ],
};

function mockLabels(data: LabelsResponse) {
  jest.spyOn(labelsAPI, 'useLabels').mockReturnValue({
    data,
    isLoading: false,
    isError: false,
    error: null,
  } as unknown as ReturnType<typeof labelsAPI.useLabels>);
}

function renderFilter(node: React.ReactNode) {
  return render(<I18nextProvider i18n={i18n}>{node}</I18nextProvider>);
}

describe('LabelFilter', () => {
  afterEach(() => jest.restoreAllMocks());

  it('adds a key=value filter once a value is picked', () => {
    mockLabels(fixture);
    const onChange = jest.fn();
    renderFilter(<LabelFilter value={{}} onChange={onChange} />);

    // Pick the key.
    fireEvent.mouseDown(screen.getAllByRole('combobox')[0]);
    fireEvent.click(screen.getByRole('option', { name: 'team' }));

    // The value picker now offers that key's values; pick one.
    fireEvent.mouseDown(screen.getAllByRole('combobox')[1]);
    fireEvent.click(screen.getByRole('option', { name: 'payments' }));

    expect(onChange).toHaveBeenCalledWith({ team: 'payments' });
  });

  it('renders active filters as chips and removes them on delete', () => {
    mockLabels(fixture);
    const onChange = jest.fn();
    renderFilter(<LabelFilter value={{ team: 'payments' }} onChange={onChange} />);

    expect(screen.getByText('team=payments')).toBeInTheDocument();

    // MUI renders the Chip delete affordance as a CancelIcon.
    fireEvent.click(screen.getByTestId('CancelIcon'));
    expect(onChange).toHaveBeenCalledWith({});
  });

  it('keeps existing filters when a second key is added', () => {
    mockLabels(fixture);
    const onChange = jest.fn();
    renderFilter(<LabelFilter value={{ team: 'payments' }} onChange={onChange} />);

    fireEvent.mouseDown(screen.getAllByRole('combobox')[0]);
    fireEvent.click(screen.getByRole('option', { name: 'env' }));
    fireEvent.mouseDown(screen.getAllByRole('combobox')[1]);
    fireEvent.click(screen.getByRole('option', { name: 'prod' }));

    expect(onChange).toHaveBeenCalledWith({ team: 'payments', env: 'prod' });
  });
});

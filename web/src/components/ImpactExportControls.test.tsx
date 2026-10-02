import { fireEvent, render, screen } from '@testing-library/react';

import { impactFixture } from '../test/impactFixture';
import { parseImpactCapture } from '../api/impactCapture';
import { downloadImpactCapture } from '../lib/impactExport';
import { ImpactExportControls } from './ImpactExportControls';
import { BlastRadiusPanel } from './BlastRadiusPanel';

jest.mock('../lib/impactExport', () => ({ downloadImpactCapture: jest.fn() }));
const download = jest.mocked(downloadImpactCapture);
beforeEach(() => download.mockReset());
const capture = (cluster = '') => parseImpactCapture(JSON.stringify(impactFixture(cluster)));

test('requires a per-capture acknowledgement and hands the exact object to both formats', () => {
  const response = capture();
  const { rerender } = render(<ImpactExportControls response={response} />);
  expect(screen.getByRole('button', { name: 'Download JSON' })).toBeDisabled();
  fireEvent.click(screen.getByRole('checkbox'));
  fireEvent.click(screen.getByRole('button', { name: 'Download JSON' }));
  fireEvent.click(screen.getByRole('button', { name: 'Download HTML' }));
  expect(download.mock.calls).toEqual([[response, 'json'], [response, 'html']]);
  expect(download.mock.calls[0][0]).toBe(response);
  expect(screen.getByRole('status')).toHaveTextContent('Download requested');
  rerender(<ImpactExportControls response={capture()} />); // Same query, new captured object.
  expect(screen.getByRole('checkbox')).not.toBeChecked();
  expect(screen.getByRole('button', { name: 'Download HTML' })).toBeDisabled();
  expect(screen.queryByRole('status')).not.toBeInTheDocument();
});

test('refresh/loading/error removes the download surface and old acknowledgement', () => {
  const response = capture();
  const props = { response, loading: false, error: null, onRefresh: jest.fn() };
  const { rerender } = render(<BlastRadiusPanel {...props} />);
  fireEvent.click(screen.getByRole('checkbox'));
  rerender(<BlastRadiusPanel {...props} loading />);
  expect(screen.queryByRole('button', { name: 'Download JSON' })).not.toBeInTheDocument();
  rerender(<BlastRadiusPanel {...props} error={new Error('private error')} />);
  expect(screen.queryByRole('button', { name: 'Download HTML' })).not.toBeInTheDocument();
  rerender(<BlastRadiusPanel {...props} response={capture('east')} />);
  expect(screen.getByRole('checkbox')).not.toBeChecked();
  expect(download).not.toHaveBeenCalled();
});

test('download preparation failure is closed and never claims that a file was saved', () => {
  download.mockImplementation(() => { throw new Error('private failure payload'); });
  render(<ImpactExportControls response={capture()} />);
  fireEvent.click(screen.getByRole('checkbox'));
  fireEvent.click(screen.getByRole('button', { name: 'Download HTML' }));
  expect(screen.getByRole('status')).toHaveTextContent('Could not prepare this download');
  expect(screen.getByRole('status')).not.toHaveTextContent('private failure payload');
  expect(download).toHaveBeenCalledTimes(1);
});

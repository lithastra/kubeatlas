import { fireEvent, render, screen } from '@testing-library/react';

import { ApiError } from '../api/client';
import { impactFixture } from '../test/impactFixture';
import { BlastRadiusPanel } from './BlastRadiusPanel';

test('server counts, representative path, limitations and retained metadata remain distinct', () => {
  const response = impactFixture();
  const refresh = jest.fn();
  render(<BlastRadiusPanel response={response} loading={false} error={null} onRefresh={refresh} />);
  expect(screen.getByText('At least 2 observed · 1 direct · 1 indirect')).toBeInTheDocument();
  const summary = screen.getByText('Service/api · demo · Indirect (2 hops)');
  fireEvent.click(summary);
  expect(summary.closest('details')).toHaveAttribute('open');
  expect(screen.getByText(/SELECTS · Stored edge/)).toBeVisible();
  expect(screen.getByText(/dynamic_source_scope_unverified/)).toBeVisible();
  expect(screen.getByText(/History: recording · Coverage: unknown/)).toBeVisible();
  expect(screen.getByText(/Latest marker record: none observed/)).toBeVisible();
  expect(screen.getByText(/not a verified backup/)).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: 'Refresh analysis' }));
  expect(refresh).toHaveBeenCalledTimes(1);
});

test('truncation and incomplete empty results are not a complete absence claim', () => {
  const response = impactFixture();
  Object.assign(response.analysis, { resources: [], truncated: true, truncationReasons: ['max_depth'], modeledTraversalComplete: false });
  response.analysis.counts = { total: 0, direct: 0, indirect: 0, lowerBound: true };
  render(<BlastRadiusPanel response={response} loading={false} error={null} onRefresh={() => {}} />);
  expect(screen.getByText('No dependents observed; analysis incomplete.')).toBeVisible();
  expect(screen.getByText(/Truncated: max_depth/)).toBeVisible();
  expect(screen.queryByText(/No dependents found/)).not.toBeInTheDocument();
});

test('loading or error hides previous success rather than presenting stale counts', () => {
  const props = { response: impactFixture(), onRefresh: jest.fn() };
  const { rerender } = render(<BlastRadiusPanel {...props} loading error={null} />);
  expect(screen.getByRole('status')).toHaveTextContent('Loading server analysis');
  expect(screen.queryByText(/2 observed/)).not.toBeInTheDocument();
  rerender(<BlastRadiusPanel {...props} loading={false} error={new ApiError(409, 'conflict', 'raw')} />);
  expect(screen.getByRole('alert')).toHaveTextContent('Resource identity changed');
  expect(screen.queryByText(/2 observed/)).not.toBeInTheDocument();
});

test('reference-only identity, stale stopped retention and independent authorization are disclosed', () => {
  const response = impactFixture();
  response.analysis.root.referenceOnly = true;
  const retention = response.analysis.availability!.evidence.history.retentionEvidence!;
  retention.stale = true;
  retention.stopped = true;
  response.analysis.authorization.applicable = true;
  response.analysis.authorization.counts.total = 9;
  render(<BlastRadiusPanel response={response} loading={false} error={null} onRefresh={() => {}} />);
  expect(screen.getByText('Reference only. Existence and values are not verified.')).toBeVisible();
  expect(screen.getByText('Retention check: observed · stale · stopped')).toBeVisible();
  fireEvent.click(screen.getByText('Authorization associations (separate)'));
  expect(screen.getByText(/9 observed/)).toBeVisible();
  expect(screen.getByText(/At least 2 observed/)).toBeVisible();
});

test('renders untrusted identity text as text, never HTML', () => {
  const response = impactFixture();
  response.analysis.root.name = '<img src=x onerror=alert(1)>';
  const { container } = render(<BlastRadiusPanel response={response} loading={false} error={null} onRefresh={() => {}} />);
  expect(screen.getByText('ConfigMap/<img src=x onerror=alert(1)>')).toBeVisible();
  expect(container.querySelector('img')).toBeNull();
});

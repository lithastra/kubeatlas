import { fireEvent, render, screen, within } from '@testing-library/react';

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
  expect(screen.queryByText('API observation details')).not.toBeInTheDocument();
  rerender(<BlastRadiusPanel {...props} loading={false} error={new ApiError(409, 'conflict', 'raw')} />);
  expect(screen.getByRole('alert')).toHaveTextContent('Resource identity changed');
  expect(screen.queryByText(/2 observed/)).not.toBeInTheDocument();
  expect(screen.queryByText('API observation details')).not.toBeInTheDocument();
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

test('API coverage explains captured inventory flags without another read', () => {
  const response = impactFixture();
  const coverage = response.analysis.observation!.ordinary;
  coverage.before!.apiInventory!.state = 'complete';
  coverage.before!.apiInventory!.stale = true;
  coverage.before!.apiInventory!.stopped = true;
  coverage.before!.apiInventory!.checkedAt = '2026-09-30T23:00:00Z';
  coverage.after!.apiInventory!.limited = true;
  coverage.optionalApis = [{ group: 'gateway.networking.k8s.io', version: 'v1', resource: 'gateways', before: 'unknown', after: 'unknown' }];
  const refresh = jest.fn();
  const raw = JSON.stringify(response);
  render(<BlastRadiusPanel response={response} loading={false} error={null} onRefresh={refresh} />);
  const summary = screen.getByText('API observation details');
  fireEvent.click(summary);
  const details = within(summary.closest('details')!);
  expect(details.getByText('Before graph read')).toBeVisible();
  expect(details.getByText('Complete enumeration · stale · stopped')).toBeVisible();
  expect(details.getByText('Partial enumeration · limited')).toBeVisible();
  expect(details.getByText('Checked: 2026-09-30T23:00:00Z')).toBeVisible();
  expect(details.getByText('Checked: 2026-10-01T00:00:00Z')).toBeVisible();
  expect(details.getByText('Before: Unknown · After: Unknown')).toBeVisible();
  expect(details.getByText(/not successful watches or effective permissions/)).toBeVisible();
  expect(refresh).not.toHaveBeenCalled();
  expect(JSON.stringify(response)).toBe(raw);
});

test('optional API versions, shapes and endpoints lacking observation evidence stay distinct', () => {
  const response = impactFixture();
  const coverage = response.analysis.observation!.ordinary;
  coverage.before!.apiInventory!.state = 'complete';
  coverage.after!.apiInventory!.state = 'complete';
  coverage.optionalApis = [
    { group: 'gateway.networking.k8s.io', version: 'v1', resource: 'gateways', before: 'not_advertised', after: 'advertised' },
    { group: 'gateway.networking.k8s.io', version: 'v1', resource: 'httproutes', before: 'other_version_advertised', after: 'not_advertised' },
    { group: 'kyverno.io', version: 'v1', resource: 'policies', before: 'unsupported_shape', after: 'not_advertised' },
  ];
  const widget = { group: 'example.test', version: 'v1', resource: 'widgets', kind: 'Widget', namespaced: true, list: true, watch: true };
  const gateway = { group: 'gateway.networking.k8s.io', version: 'v1', resource: 'gateways', kind: 'Gateway', namespaced: true, list: true, watch: true };
  coverage.before!.apiInventory!.resources = [widget, { ...gateway, resource: 'httproutes', version: 'v1beta1', kind: 'HTTPRoute' },
    { ...widget, group: 'kyverno.io', resource: 'policies', kind: 'Policy', watch: false }];
  coverage.after!.apiInventory!.resources = [widget, gateway];
  coverage.unobservedApis = [widget, gateway];
  render(<BlastRadiusPanel response={response} loading={false} error={null} onRefresh={() => {}} />);
  const summary = screen.getByText('API observation details');
  fireEvent.click(summary);
  const details = within(summary.closest('details')!);
  expect(details.getByText('Before: Not advertised · After: Advertised')).toBeVisible();
  expect(details.getByText('Before: Other version advertised · After: Not advertised')).toBeVisible();
  expect(details.getByText('Before: Unsupported endpoint shape · After: Not advertised')).toBeVisible();
  expect(details.getByText('example.test/v1/widgets')).toBeVisible();
  expect(details.getByText('Widget · namespaced')).toBeVisible();
  expect(details.getByText(/not an uninstalled operator/)).toBeVisible();
});

test.each([
  ['unknown', 'Unknown'], ['complete', 'Complete enumeration'], ['partial', 'Partial enumeration'],
  ['permission_denied', 'Discovery permission denied'], ['failed', 'Discovery failed'],
])('inventory %s never upgrades an incomplete empty result', (state, label) => {
  const response = impactFixture();
  response.analysis.resources = [];
  response.analysis.counts = { total: 0, direct: 0, indirect: 0, lowerBound: false };
  response.analysis.observation!.ordinary.before!.apiInventory!.state = state;
  if (state === 'unknown') {
    response.analysis.observation!.ordinary.before!.apiInventory!.checkedAt = '0001-01-01T00:00:00Z';
    response.analysis.observation!.ordinary.before!.apiInventory!.revision = 0;
  }
  if (['unknown', 'permission_denied', 'failed'].includes(state)) response.analysis.observation!.ordinary.before!.apiInventory!.resources = [];
  response.analysis.observation!.ordinary.after!.apiInventory = undefined;
  render(<BlastRadiusPanel response={response} loading={false} error={null} onRefresh={() => {}} />);
  const summary = screen.getByText('API observation details');
  fireEvent.click(summary);
  const details = within(summary.closest('details')!);
  expect(details.getByText(label, { exact: true })).toBeVisible();
  if (state === 'unknown') expect(details.getByText('Checked: none observed / unknown')).toBeVisible();
  expect(details.getByText('Inventory unavailable')).toBeVisible();
  expect(details.getByText('Optional API assessments are unavailable.')).toBeVisible();
  expect(details.getByText(/This does not establish complete collector coverage/)).toBeVisible();
  expect(screen.getByText('No dependents observed; analysis incomplete.')).toBeVisible();
  expect(screen.queryByText(/No dependents found/)).not.toBeInTheDocument();
});

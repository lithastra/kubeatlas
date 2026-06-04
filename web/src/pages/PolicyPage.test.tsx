import type { UseQueryResult } from '@tanstack/react-query';
import { fireEvent, render, screen } from '@testing-library/react';
import { I18nextProvider } from 'react-i18next';
import { MemoryRouter } from 'react-router-dom';

import { ApiError } from '../api/client';
import * as policyAPI from '../api/policy';
import type { ConstraintAffectedResponse, PolicyConstraint } from '../api/types';
import i18n from '../i18n';
import { PolicyPage } from './PolicyPage';

function asList(data?: PolicyConstraint[], err?: ApiError) {
  return {
    data,
    error: err ?? null,
    isPending: !data && !err,
    isError: !!err,
    isSuccess: !!data,
  } as unknown as UseQueryResult<PolicyConstraint[], ApiError>;
}

function asAffected(data?: ConstraintAffectedResponse, err?: ApiError) {
  return {
    data,
    error: err ?? null,
    isPending: !data && !err,
    isError: !!err,
    isSuccess: !!data,
  } as unknown as UseQueryResult<ConstraintAffectedResponse, ApiError>;
}

function renderPage() {
  return render(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter>
        <PolicyPage />
      </MemoryRouter>
    </I18nextProvider>,
  );
}

const fixture: PolicyConstraint[] = [
  { name: 'require-labels', kind: 'ClusterPolicy', engine: 'kyverno', violations: 5 },
  { name: 'all-have-owner', kind: 'K8sRequiredLabels', engine: 'gatekeeper', violations: 0 },
];

describe('PolicyPage', () => {
  afterEach(() => jest.restoreAllMocks());

  it('renders constraints from the API', () => {
    jest.spyOn(policyAPI, 'useConstraints').mockReturnValue(asList(fixture));
    jest.spyOn(policyAPI, 'useConstraintAffected').mockReturnValue(asAffected(undefined));

    renderPage();
    expect(screen.getByText('require-labels')).toBeInTheDocument();
    expect(screen.getByText('all-have-owner')).toBeInTheDocument();
    expect(screen.getByText('5')).toBeInTheDocument();
  });

  it('renders an error alert on API failure', () => {
    const err = new ApiError(500, 'internal', 'boom');
    jest.spyOn(policyAPI, 'useConstraints').mockReturnValue(asList(undefined, err));
    jest.spyOn(policyAPI, 'useConstraintAffected').mockReturnValue(asAffected(undefined));

    renderPage();
    expect(screen.getByText(/boom/i)).toBeInTheDocument();
  });

  it('renders the empty state when there are no constraints', () => {
    jest.spyOn(policyAPI, 'useConstraints').mockReturnValue(asList([]));
    jest.spyOn(policyAPI, 'useConstraintAffected').mockReturnValue(asAffected(undefined));

    renderPage();
    expect(screen.getByText(/No policy constraints found/i)).toBeInTheDocument();
  });

  it('shows affected resources after selecting a constraint', () => {
    jest.spyOn(policyAPI, 'useConstraints').mockReturnValue(asList(fixture));
    jest.spyOn(policyAPI, 'useConstraintAffected').mockReturnValue(
      asAffected({
        constraint: 'require-labels',
        count: 1,
        resources: [
          {
            resource: { kind: 'Deployment', namespace: 'demo', name: 'web' },
            violated: true,
            message: 'missing label app',
          },
        ],
      }),
    );

    renderPage();
    fireEvent.click(screen.getByText('require-labels'));
    expect(screen.getByText('demo/Deployment/web')).toBeInTheDocument();
    expect(screen.getByText('missing label app')).toBeInTheDocument();
  });
});

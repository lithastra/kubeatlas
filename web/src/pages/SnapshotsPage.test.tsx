import { render, screen } from '@testing-library/react';
import { I18nextProvider } from 'react-i18next';
import { MemoryRouter } from 'react-router-dom';
import type { UseQueryResult } from '@tanstack/react-query';

import { SnapshotsPage } from './SnapshotsPage';
import i18n from '../i18n';
import * as snapshotsAPI from '../api/snapshots';
import { ApiError } from '../api/client';
import type { DiffResult, SnapshotListResponse } from '../api/types';

function asListResult(data?: SnapshotListResponse, err?: ApiError) {
  return {
    data,
    error: err ?? null,
    isPending: !data && !err,
    isError: !!err,
    isSuccess: !!data,
  } as unknown as UseQueryResult<SnapshotListResponse, ApiError>;
}

function asDiffResult(data?: DiffResult, err?: ApiError) {
  return {
    data,
    error: err ?? null,
    isPending: !data && !err,
    isError: !!err,
    isSuccess: !!data,
  } as unknown as UseQueryResult<DiffResult, ApiError>;
}

function renderPage() {
  return render(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter>
        <SnapshotsPage />
      </MemoryRouter>
    </I18nextProvider>,
  );
}

describe('SnapshotsPage', () => {
  afterEach(() => jest.restoreAllMocks());

  it('renders the not-enabled banner when the server returns 503', () => {
    const err = new ApiError(503, 'unavailable', 'snapshots not enabled');
    jest.spyOn(snapshotsAPI, 'useSnapshots').mockReturnValue(asListResult(undefined, err));
    jest.spyOn(snapshotsAPI, 'useSnapshotDiff').mockReturnValue(asDiffResult(undefined, err));

    renderPage();
    expect(screen.getByText(/Snapshots are not enabled/i)).toBeInTheDocument();
  });

  it('renders the diff with three event sections and the timeline', () => {
    const snapshots: SnapshotListResponse = {
      snapshots: [
        {
          id: 2,
          ts: '2026-05-21T10:00:00Z',
          resourceCount: 1234,
          edgeCount: 2345,
          durationMs: 1500,
          trigger: 'periodic',
        },
        {
          id: 1,
          ts: '2026-05-21T09:00:00Z',
          resourceCount: 1200,
          edgeCount: 2300,
          durationMs: 980,
          trigger: 'startup',
        },
      ],
    };
    const diff: DiffResult = {
      from: '2026-05-21T09:00:00Z',
      to: '2026-05-21T10:00:00Z',
      added: [
        { namespace: 'petclinic', kind: 'Deployment', name: 'frontend', eventType: 'add', ts: '2026-05-21T09:30:00Z' },
      ],
      modified: [
        { namespace: 'petclinic', kind: 'ConfigMap', name: 'app-config', eventType: 'update', ts: '2026-05-21T09:45:00Z' },
      ],
      removed: [
        { namespace: 'petclinic', kind: 'Pod', name: 'frontend-old-1', eventType: 'delete', ts: '2026-05-21T09:50:00Z' },
      ],
    };
    jest.spyOn(snapshotsAPI, 'useSnapshots').mockReturnValue(asListResult(snapshots));
    jest.spyOn(snapshotsAPI, 'useSnapshotDiff').mockReturnValue(asDiffResult(diff));

    renderPage();

    // Sections: added / modified / removed all rendered with counts in the heading.
    expect(screen.getByText(/Added \(1\)/)).toBeInTheDocument();
    expect(screen.getByText(/Modified \(1\)/)).toBeInTheDocument();
    expect(screen.getByText(/Removed \(1\)/)).toBeInTheDocument();

    // Diff rows: added + modified entries are links; the removed entry is plain text.
    const added = screen.getByRole('link', { name: 'frontend' });
    expect(added.getAttribute('href')).toBe('/resources/petclinic/Deployment/frontend');
    expect(screen.queryByRole('link', { name: 'frontend-old-1' })).toBeNull();

    // Timeline row count tracks snapshots.length.
    expect(screen.getByText(/Recent full-sync snapshots/i)).toBeInTheDocument();
    expect(screen.getByText('1,234')).toBeInTheDocument(); // resourceCount formatted
    expect(screen.getByText('1.5s')).toBeInTheDocument(); // durationMs formatted
  });

  it('shows an empty-section hint when a category has no entries', () => {
    jest
      .spyOn(snapshotsAPI, 'useSnapshots')
      .mockReturnValue(asListResult({ snapshots: [] }));
    jest.spyOn(snapshotsAPI, 'useSnapshotDiff').mockReturnValue(
      asDiffResult({
        from: '2026-05-21T09:00:00Z',
        to: '2026-05-21T10:00:00Z',
        added: [],
        modified: [],
        removed: [],
      }),
    );
    renderPage();
    // Three "No matches." hints — one per empty section.
    expect(screen.getAllByText(/No matches/i)).toHaveLength(3);
  });
});

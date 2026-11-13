import { render, screen, waitFor } from '@testing-library/react';
import { I18nextProvider } from 'react-i18next';
import { MemoryRouter } from 'react-router-dom';

import { fetchJSON } from '../api/client';
import i18n from '../i18n';
import { TopBar } from './TopBar';

jest.mock('../api/client', () => ({ fetchJSON: jest.fn() }));
jest.mock('./ThemeSwitcher', () => ({ ThemeSwitcher: () => null }));
const fetchInfo = jest.mocked(fetchJSON);
function mount(version?: string) {
  return render(<MemoryRouter><I18nextProvider i18n={i18n}><TopBar version={version} /></I18nextProvider></MemoryRouter>);
}
beforeEach(() => fetchInfo.mockReset());

test('shows the running server version instead of a hardcoded release', async () => {
  fetchInfo.mockResolvedValue({ version: '1.7.0' });
  mount();
  await waitFor(() => expect(screen.getByTestId('server-version')).toHaveTextContent(/^1\.7\.0$/));
  expect(fetchInfo).toHaveBeenCalledWith('/api/v1/info', { signal: expect.any(AbortSignal) });
});

test.each([{}, { version: 17 }, { version: '' }, { version: '  ' }])('does not invent a version from malformed metadata %j', async (info) => {
  fetchInfo.mockResolvedValue(info);
  mount();
  await waitFor(() => expect(fetchInfo).toHaveBeenCalledTimes(1));
  expect(screen.getByTestId('server-version')).toHaveTextContent('Version unavailable');
});

test('keeps unavailable metadata explicit and cancels the request on unmount', async () => {
  fetchInfo.mockRejectedValue(new Error('unavailable'));
  const view = mount();
  await waitFor(() => expect(fetchInfo).toHaveBeenCalledTimes(1));
  expect(screen.getByTestId('server-version')).toHaveTextContent('Version unavailable');
  const signal = fetchInfo.mock.calls[0][1]?.signal;
  view.unmount();
  expect(signal?.aborted).toBe(true);
});

test('honors an explicit embedding version without a metadata request', () => {
  mount('1.7.0-rc.1');
  expect(screen.getByTestId('server-version')).toHaveTextContent(/^1\.7\.0-rc\.1$/);
  expect(fetchInfo).not.toHaveBeenCalled();
});

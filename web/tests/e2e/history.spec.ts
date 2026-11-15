import { expect, test, type Page, type Route } from '@playwright/test';

const evidence = '/tmp/kubeatlas-history-client-browser';
const result = (cluster: string) => ({ from: '2026-10-20T00:00:00Z', to: '2026-10-20T01:00:00Z', added: [], removed: [], modified: [{ namespace: 'demo', kind: 'Pod', name: `${cluster || 'standalone'}-only`, uid: 'shared-uid', eventType: 'update', ts: '2026-10-20T00:30:00Z' }] });
async function fixture(page: Page, mode: 'single' | 'federated', reply?: (route: Route, url: URL) => Promise<void>, clusters = ['east', 'west']) {
  const reads: URL[] = []; const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('console', (message) => { if (message.type() === 'error') errors.push(message.text()); });
  await page.routeWebSocket('**/api/v1alpha1/watch', () => {});
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/v1/federation/clusters') return route.fulfill({ json: { mode, clusters: mode === 'single' ? [] : clusters } });
    if (url.pathname.startsWith('/api/v1/snapshots')) {
      reads.push(url); expect(route.request().method()).toBe('GET');
      if (reply) return reply(route, url);
      if (url.pathname.endsWith('/diff')) return route.fulfill({ json: result(url.searchParams.get('cluster') || '') });
      return route.fulfill({ json: { snapshots: [], count: 0 } });
    }
    return route.fulfill({ json: { nodes: [], edges: [], resources: [], keys: [], values: [] } });
  });
  return { reads, errors };
}
for (const [label, viewport] of [['desktop', { width: 1280, height: 800 }], ['mobile', { width: 390, height: 844 }]] as const) {
  test(`${label}: member selection, switching and all-members clearing`, async ({ page }) => {
    await page.setViewportSize(viewport);
    const { reads, errors } = await fixture(page, 'federated');
    await page.goto('/snapshots');
    await expect(page).toHaveTitle(/KubeAtlas/i);
    await expect(page.getByRole('heading', { name: 'Snapshots', exact: true })).toBeVisible();
    await expect(page.getByText('Select one attached cluster in the cluster strip to read its history.')).toBeVisible();
    expect(reads).toHaveLength(0);
    await page.getByRole('button', { name: 'Focus cluster east', exact: true }).click();
    await expect(page.getByText('History scope: east', { exact: true })).toBeVisible();
    await expect(page.getByText('east-only', { exact: true })).toBeVisible();
    expect(reads).toHaveLength(2);
    expect(reads.every((url) => url.searchParams.getAll('cluster').join(',') === 'east')).toBe(true);
    await expect(page.getByRole('link', { name: 'east-only', exact: true })).toHaveCount(0);
    await page.screenshot({ path: `${evidence}/history-${label}.png`, fullPage: false });
    await page.getByRole('button', { name: 'Focus cluster west', exact: true }).click();
    await expect(page.getByText('west-only', { exact: true })).toBeVisible();
    await expect(page.getByText('east-only', { exact: true })).toHaveCount(0);
    expect(reads.filter((url) => url.searchParams.get('cluster') === 'west')).toHaveLength(2);
    await page.getByRole('button', { name: 'All clusters', exact: true }).click();
    await expect(page.getByText('Select one attached cluster in the cluster strip to read its history.')).toBeVisible();
    await expect(page.getByText('west-only', { exact: true })).toHaveCount(0);
    expect(reads).toHaveLength(4); expect(errors).toEqual([]);
    await expect(page.locator('vite-error-overlay')).toHaveCount(0);
  });
}
test('denied member hides previous data with no retry or fallback', async ({ page }) => {
  const { reads, errors } = await fixture(page, 'federated', async (route, url) => {
    if (url.searchParams.get('cluster') === 'west') return route.fulfill({ status: 403, json: { code: 'forbidden', error: 'Access denied for this member.' } });
    return route.fulfill({ json: url.pathname.endsWith('/diff') ? result('east') : { snapshots: [], count: 0 } });
  });
  await page.goto('/snapshots');
  await page.getByRole('button', { name: 'Focus cluster east', exact: true }).click();
  await expect(page.getByText('east-only', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Focus cluster west', exact: true }).click();
  await expect(page.getByText('Access denied for this member.').first()).toBeVisible();
  await expect(page.getByText('east-only', { exact: true })).toHaveCount(0);
  await expect(page.getByText(/History is unavailable on this server/)).toHaveCount(0);
  await page.waitForTimeout(150);
  expect(reads).toHaveLength(4); expect(reads.every((url) => url.searchParams.has('cluster'))).toBe(true);
  expect(errors.every((message) => /Failed to load resource.*403/.test(message))).toBe(true);
});
test('standalone is explicit and an empty federation stays blocked', async ({ page }) => {
  const local = await fixture(page, 'single');
  await page.goto('/snapshots');
  await expect(page.getByText('standalone-only', { exact: true })).toBeVisible();
  expect(local.reads).toHaveLength(2); expect(local.reads.every((url) => !url.searchParams.has('cluster'))).toBe(true); expect(local.errors).toEqual([]);
  await page.unroute('**/api/**');
  const empty = await fixture(page, 'federated', undefined, []);
  await page.reload();
  await expect(page.getByText('Select one attached cluster in the cluster strip to read its history.')).toBeVisible();
  expect(empty.reads).toHaveLength(0); expect(empty.errors).toEqual([]);
});

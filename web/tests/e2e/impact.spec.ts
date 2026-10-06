import { readFile } from 'node:fs/promises';
import { readFileSync } from 'node:fs';
import { expect, test, type Page, type Route } from '@playwright/test';

const matrix = JSON.parse(readFileSync(new URL('../../../test/fixtures/impact-analysis/client-matrix.json', import.meta.url), 'utf8')) as typeof import('../../../test/fixtures/impact-analysis/client-matrix.json');
const captures = JSON.parse(readFileSync(new URL('../../../test/fixtures/impact-analysis/client-captures.json', import.meta.url), 'utf8')) as typeof import('../../../test/fixtures/impact-analysis/client-captures.json');

// The real production application, HTTP reader, controls and browser download
// sink run here. Only API/WS transport is replaced. Go tests lock these shared
// captures to the actual handler/store/engine. This is not cluster acceptance.
type Reply = (route: Route, url: URL) => Promise<void>;
// Preserve semantics of the Go-locked fixtures while making reserialization
// observably wrong: exports must retain the HTTP whitespace and final newline.
const wire = (name: keyof typeof captures) => `${JSON.stringify(captures[name], null, 2)}\n`;
const ordinary = wire('ordinary');

// UI-only metadata scenario: retain the shared graph/path oracle, while using
// the documented stale-before/fresh-after API-inventory contract. It does not
// stand in for a real discovery collector or cluster acceptance.
function inventoryCapture() {
  const capture = JSON.parse(ordinary);
  const coverage = capture.analysis.observation.ordinary;
  const resources = [
    { group: 'gateway.networking.k8s.io', version: 'v1', resource: 'gateways', kind: 'Gateway', namespaced: true, list: true, watch: true },
    { group: 'example.test', version: 'v1', resource: 'widgets', kind: 'Widget', namespaced: true, list: true, watch: true },
  ];
  coverage.before.apiInventory = { clusterId: '', generation: 1, revision: 1, state: 'complete',
    checkedAt: '2026-09-30T23:00:00Z', stale: true, stopped: false, limited: false, resources };
  coverage.after.apiInventory = { ...coverage.before.apiInventory, revision: 2, checkedAt: coverage.after.capturedAt, stale: false };
  coverage.optionalApis = [
    ['gateway.networking.k8s.io', 'v1', 'gateways'], ['gateway.networking.k8s.io', 'v1', 'httproutes'],
    ['kyverno.io', 'v1', 'policies'], ['kyverno.io', 'v1', 'clusterpolicies'],
    ['wgpolicyk8s.io', 'v1alpha2', 'policyreports'], ['wgpolicyk8s.io', 'v1alpha2', 'clusterpolicyreports'],
    ['route.openshift.io', 'v1', 'routes'],
  ].map(([group, version, resource]) => ({ group, version, resource, before: 'unknown',
    after: resource === 'gateways' ? 'advertised' : 'not_advertised' }));
  coverage.unobservedApis = resources;
  coverage.reasons.push('api_inventory_stale', 'api_inventory_window_unstable', 'advertised_resource_types_unobserved');
  return `${JSON.stringify(capture, null, 2)}\n`;
}

async function fixtureTransport(page: Page, reply?: Reply) {
  const requests: URL[] = [];
  const unexpected: string[] = [];
  await page.routeWebSocket('**/api/v1alpha1/watch', () => {});
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    const json = (body: unknown) => route.fulfill({ json: body });
    if (url.pathname.startsWith('/api/v1/impact/')) {
      requests.push(url);
      expect(route.request().method()).toBe('GET');
      expect(url.searchParams.get('limit')).toBe('200');
      const root = matrix.resources.find((item) => url.pathname === `/api/v1/impact/${item.namespace}/${item.kind}/${item.name}`);
      expect(root).toBeDefined();
      if (url.searchParams.get('cluster')) {
        // Federated topology has no previously captured UID and must not read
        // a same-named standalone detail to invent one.
        expect(url.searchParams.has('expected_uid')).toBe(false);
      } else {
        expect(url.searchParams.get('expected_uid')).toBe(root!.uid);
      }
      if (reply) return reply(route, url);
      const entry = matrix.cases.find((item) =>
        url.pathname === `/api/v1/impact/${item.namespace}/${item.kind}/${item.rootName}` &&
        (url.searchParams.get('cluster') || '') === item.cluster &&
        url.searchParams.get('relation') === item.relation &&
        url.searchParams.get('max_depth') === String(item.depth));
      // The Service flow opens in the application's default dependents mode.
      // That query is outside this matrix; fail it explicitly until the user
      // selects dependencies rather than inventing another successful oracle.
      if (!entry && url.pathname === '/api/v1/impact/other/Service/api' && url.searchParams.get('relation') === 'dependents') {
        return route.fulfill({ status: 404, json: { error: 'No synthetic capture for this query' } });
      }
      expect(entry, 'every successful query must have an explicit shared oracle').toBeDefined();
      return route.fulfill({ contentType: 'application/json', body: wire(entry!.name as keyof typeof captures) });
    }
    if (url.pathname === '/api/v1/federation/clusters') return json({ clusters: ['east', 'west'] });
    if (url.pathname === '/api/v1/federation/graph') {
      const clusterId = url.searchParams.get('cluster');
      return json({ level: 'resource', clusters: [clusterId], edges: [], nodes: [{
        id: `${clusterId}:demo/ConfigMap/settings`, clusterId, type: 'resource',
        namespace: 'demo', kind: 'ConfigMap', name: 'settings',
      }] });
    }
    if (url.pathname.startsWith('/api/v1alpha1/resources/')) {
      const resource = matrix.resources.find((item) => url.pathname === `/api/v1alpha1/resources/${item.namespace}/${item.kind}/${item.name}`);
      expect(resource).toBeDefined();
      return json({ resource, incoming: [], outgoing: [] });
    }
    if (url.pathname === '/api/v1alpha1/graph') return json({ level: 'resource', nodes: [], edges: [] });
    if (url.pathname === '/api/v1alpha1/labels') return json({ labels: [], count: 0 });
    if (url.pathname === '/api/v1alpha1/namespaces') return json({ namespaces: ['demo', 'other', 'grant'] });
    unexpected.push(url.pathname);
    return route.fulfill({ status: 404, json: { error: 'Unmodeled synthetic endpoint' } });
  });
  return { requests, unexpected };
}

test.beforeEach(async ({ page }) => {
  // Errors in the rendered production bundle must fail the flow, not disappear
  // behind valid fixture assertions. HTTP failures below are intentional.
  page.on('pageerror', (error) => { throw error; });
});

async function openResource(page: Page, namespace = 'demo', kind = 'ConfigMap', name = 'settings') {
  await page.goto(`/resources/${namespace}/${kind}/${name}`);
  await expect(page.getByTestId('resource-detail-header')).toContainText(`${kind}/${name}`);
  await page.getByRole('button', { name: 'Analyze impact', exact: true }).click();
}

async function choose(page: Page, label: string, option: string) {
  await page.getByRole('combobox', { name: new RegExp(`^${label} `) }).click();
  await page.getByRole('option', { name: option, exact: true }).click();
}

async function downloaded(page: Page, format: 'JSON' | 'HTML') {
  const event = page.waitForEvent('download');
  await page.getByRole('button', { name: `Download ${format}`, exact: true }).click();
  const download = await event;
  expect(download.suggestedFilename()).toMatch(new RegExp(`^kubeatlas-impact-.*\\.${format.toLowerCase()}$`));
  const path = await download.path();
  expect(path).not.toBeNull();
  return readFile(path!, 'utf8');
}

for (const viewport of [{ width: 1280, height: 900 }, { width: 360, height: 800 }]) {
  test(`API inventory details at ${viewport.width}px preserve capture and query count`, async ({ page }, testInfo) => {
    await page.setViewportSize(viewport);
    const consoleIssues: string[] = [];
    page.on('console', (message) => { if (message.type() === 'error' || message.type() === 'warning') consoleIssues.push(message.text()); });
    const raw = inventoryCapture();
    const transport = await fixtureTransport(page, (route) => route.fulfill({ contentType: 'application/json', body: raw }));
    await openResource(page);
    await expect(page).toHaveTitle(/KubeAtlas/);
    await expect(page.getByText('2 observed · 1 direct · 1 indirect', { exact: true })).toBeVisible();
    await expect(page.locator('vite-error-overlay')).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    const summary = page.getByText('API observation details', { exact: true });
    await summary.focus();
    await summary.press('Enter');
    const details = page.locator('details').filter({ has: summary });
    await expect(details).toHaveAttribute('open', '');
    await expect(details.getByText('Complete enumeration · stale', { exact: true })).toBeVisible();
    await expect(details.getByText('Before: Unknown · After: Advertised', { exact: true })).toBeVisible();
    await expect(details.getByText('example.test/v1/widgets', { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    const bounds = await details.boundingBox();
    expect(bounds).not.toBeNull();
    expect(bounds!.x).toBeGreaterThanOrEqual(0);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(viewport.width);
    await summary.evaluate((element) => element.scrollIntoView({ block: 'start' }));
    await testInfo.attach(`API inventory ${viewport.width}px`, { body: await page.screenshot(), contentType: 'image/png' });
    await page.getByRole('checkbox', { name: 'I understand this file contains sensitive cluster topology.' }).check();
    expect(await downloaded(page, 'JSON')).toBe(raw);
    expect(transport.requests).toHaveLength(1);
    expect(transport.unexpected).toEqual([]);
    expect(consoleIssues).toEqual([]);
  });
}

for (const entry of matrix.cases.filter((item) => !item.cluster)) {
  test(`${entry.name}: resource selection, evidence and exact browser exports`, async ({ page }) => {
    const transport = await fixtureTransport(page);
    await openResource(page, entry.namespace, entry.kind, entry.rootName);
    if (entry.depth !== 5) await choose(page, 'Maximum depth', String(entry.depth));
    if (entry.relation === 'dependencies') await choose(page, 'Relation', 'Dependencies — resources this depends on');
    const counts = `${entry.truncated ? 'At least ' : ''}${entry.ordinary.length} observed · ${entry.direct} direct · ${entry.ordinary.length - entry.direct} indirect`;
    await expect(page.getByText(counts, { exact: true })).toBeVisible();
    if (entry.truncated) await expect(page.getByText('Truncated: max_depth. Counts are lower bounds.', { exact: true })).toBeVisible();
    if (!entry.ordinary.length) await expect(page.getByText(`No ${entry.relation} observed; analysis incomplete.`, { exact: true })).toBeVisible();
    const capture = captures[entry.name as keyof typeof captures];
    for (const match of capture.analysis.resources) {
      const summary = page.locator('summary').filter({ hasText: `${match.resource.kind}/${match.resource.name} ·` });
      await summary.click();
      const details = page.locator('details').filter({ has: summary });
      for (const step of match.path) await expect(details.getByText(`${step.traversalFrom} → ${step.traversalTo}`, { exact: true })).toBeVisible();
      if (match.resource.referenceOnly) await expect(summary).toContainText('reference only');
    }
    await page.getByText('Authorization associations (separate)', { exact: true }).click();
    await expect(page.getByText('Not effective permissions, credential use, or ordinary impact counts.', { exact: true })).toBeVisible();
    if (entry.authorization.length) await expect(page.getByText('3 observed · 1 direct · 2 indirect', { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Download JSON' })).toBeDisabled();
    await page.getByRole('checkbox', { name: 'I understand this file contains sensitive cluster topology.' }).check();
    const before = transport.requests.length;
    const raw = wire(entry.name as keyof typeof captures);
    expect(await downloaded(page, 'JSON')).toBe(raw);
    const html = await downloaded(page, 'HTML');
    const embedded = await page.evaluate((text) => {
      const doc = new DOMParser().parseFromString(text, 'text/html');
      return { raw: doc.getElementById('captured-json')?.textContent,
        external: doc.querySelectorAll('script,iframe,object,embed,link,img,form,[src],[href]').length,
        csp: doc.querySelector('meta[http-equiv="Content-Security-Policy"]')?.getAttribute('content') };
    }, html);
    expect(embedded).toEqual({ raw, external: 0, csp: "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'" });
    expect(transport.requests).toHaveLength(before);
    const query = transport.requests.at(-1)!;
    expect(query.searchParams.get('expected_uid')).toBe(entry.uid);
    expect(query.searchParams.get('limit')).toBe('200');
    expect(transport.unexpected).toEqual([]);
  });
}

test('refresh hides the old capture while loading and fails closed on authorization denial', async ({ page }) => {
  let calls = 0;
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => { release = resolve; });
  const transport = await fixtureTransport(page, async (route) => {
    if (++calls === 1) return route.fulfill({ contentType: 'application/json', body: ordinary });
    await gate;
    return route.fulfill({ status: 403, json: { error: 'denied', code: 'forbidden' } });
  });
  await openResource(page);
  await expect(page.getByText('2 observed · 1 direct · 1 indirect', { exact: true })).toBeVisible();
  await page.getByRole('checkbox', { name: 'I understand this file contains sensitive cluster topology.' }).check();
  await page.getByRole('button', { name: 'Refresh analysis' }).click();
  await expect(page.getByText('Loading server analysis…', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Download JSON' })).toHaveCount(0);
  release();
  await expect(page.getByText('Access to this cluster is not authorized.', { exact: true })).toBeVisible();
  await expect(page.getByText('2 observed · 1 direct · 1 indirect', { exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Download HTML' })).toHaveCount(0);
  expect(transport.requests).toHaveLength(2);
  expect(transport.unexpected).toEqual([]);
});

test('identity mismatch cannot display or export a substituted resource', async ({ page }) => {
  const transport = await fixtureTransport(page, (route) => route.fulfill({ contentType: 'application/json', body: JSON.stringify(captures.dependencies) }));
  await openResource(page);
  await expect(page.getByText('Could not obtain a matching impact result. No analysis is shown.', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Download JSON' })).toHaveCount(0);
  expect(transport.requests).toHaveLength(1);
  expect(transport.unexpected).toEqual([]);
});

test('query changes reset consent and keep depth truncation and incomplete emptiness qualified', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const transport = await fixtureTransport(page);
  await openResource(page);
  await expect(page.getByText('2 observed · 1 direct · 1 indirect', { exact: true })).toBeVisible();
  await page.getByRole('checkbox', { name: 'I understand this file contains sensitive cluster topology.' }).check();
  await choose(page, 'Maximum depth', '1');
  await expect(page.getByText('At least 1 observed · 1 direct · 0 indirect', { exact: true })).toBeVisible();
  await expect(page.getByRole('checkbox', { name: 'I understand this file contains sensitive cluster topology.' })).not.toBeChecked();
  await expect(page.getByRole('button', { name: 'Download JSON' })).toBeDisabled();
  await choose(page, 'Maximum depth', '5');
  await choose(page, 'Relation', 'Dependencies — resources this depends on');
  await expect(page.getByText('No dependencies observed; analysis incomplete.', { exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  expect(transport.requests).toHaveLength(4);
  expect(transport.unexpected).toEqual([]);
});

test('federated topology clears east on switch, rejects its late refresh and fails closed for denied west', async ({ page }) => {
  let eastCalls = 0;
  let release: () => void = () => {};
  let attempted: () => void = () => {};
  const gate = new Promise<void>((resolve) => { release = resolve; });
  const lateReply = new Promise<void>((resolve) => { attempted = resolve; });
  const transport = await fixtureTransport(page, async (route, url) => {
    if (url.searchParams.get('cluster') === 'west') return route.fulfill({ status: 403, json: { error: 'denied', code: 'forbidden' } });
    expect(url.searchParams.get('cluster')).toBe('east');
    if (++eastCalls === 2) await gate;
    try {
      await route.fulfill({ contentType: 'application/json', body: wire('federated') });
    } finally {
      if (eastCalls === 2) attempted();
    }
  });
  await page.goto('/topology');
  await page.getByRole('button', { name: 'Focus cluster east', exact: true }).click();
  const canvas = page.getByTestId('topology-canvas');
  await expect(canvas.locator('canvas')).toHaveCount(3);
  await canvas.focus();
  await canvas.press('ArrowRight');
  await canvas.press('Enter');
  await page.getByRole('button', { name: /Analyze impact/ }).click();
  await expect(page.getByText('Cluster: east · All namespaces', { exact: true })).toBeVisible();
  await page.getByRole('checkbox', { name: 'I understand this file contains sensitive cluster topology.' }).check();
  expect(await downloaded(page, 'JSON')).toBe(wire('federated'));
  await page.getByRole('button', { name: 'Refresh analysis' }).click();
  await expect(page.getByText('Loading server analysis…', { exact: true })).toBeVisible();
  await expect.poll(() => eastCalls).toBe(2);
  await page.getByRole('button', { name: 'Focus cluster west', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Impact analysis', exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Download JSON' })).toHaveCount(0);
  await expect(page.getByText('Cluster: east · All namespaces', { exact: true })).toHaveCount(0);
  release();
  await lateReply;
  await canvas.focus();
  await canvas.press('ArrowRight');
  await canvas.press('Enter');
  await page.getByRole('button', { name: /Analyze impact/ }).click();
  await expect(page.getByText('Access to this cluster is not authorized.', { exact: true })).toBeVisible();
  await expect(page.getByText('Cluster: east · All namespaces', { exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Download JSON' })).toHaveCount(0);
  expect(transport.requests).toHaveLength(3);
  expect(transport.requests[0].searchParams.get('cluster')).toBe('east');
  expect(transport.requests[1].searchParams.get('cluster')).toBe('east');
  expect(transport.requests[2].searchParams.get('cluster')).toBe('west');
  expect(transport.unexpected).toEqual([]);
});

import { execFile } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { readFile, stat, access } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import type { ImpactResponse } from '../../src/api/impactTypes';

const matrix = JSON.parse(readFileSync(new URL('../../../test/fixtures/impact-analysis/client-matrix.json', import.meta.url), 'utf8')) as typeof import('../../../test/fixtures/impact-analysis/client-matrix.json');
type Entry = typeof matrix.cases[number];
type Capture = typeof import('../../../test/fixtures/impact-analysis/client-captures.json')['ordinary'];
const exec = promisify(execFile);
const cli = fileURLToPath(new URL('../../../bin/kubectl-atlas', import.meta.url));
const local = 'http://127.0.0.1:4174';
const federated = 'http://127.0.0.1:4175';
const token = 'synthetic-matrix-east';

// Nothing is routed, fulfilled or substituted in this suite: production Web
// assets, API, WS and the actual CLI all talk to the same real Go HTTP fixture.
// Only the graph and RBAC configuration are synthetic; no Kubernetes is used.
test.beforeEach(async ({ page, request }) => {
  page.on('pageerror', (error) => { throw error; });
  await expect.poll(async () => (await request.get(`${federated}/healthz`)).status()).toBe(200);
});

function query(entry: Entry) {
  const q = new URLSearchParams({ relation: entry.relation, max_depth: String(entry.depth), limit: '200', expected_uid: entry.uid });
  if (entry.cluster) q.set('cluster', entry.cluster);
  return `/api/v1/impact/${entry.namespace}/${entry.kind}/${entry.rootName}?${q}`;
}

// Separate HTTP requests have separate clocks. No other field, identity,
// count, qualification, version or complete evidence record is normalized.
function withoutCaptureClocks(raw: string) {
  const capture = JSON.parse(raw, (_key, value: unknown) => {
    // This fixture uses bounded integers. Refuse future unsafe numeric shapes
    // rather than letting JS rounding make unequal Go wire values seem equal.
    if (typeof value === 'number' && !Number.isSafeInteger(value)) throw new Error('Unsafe numeric fixture comparison');
    return value;
  }) as Capture;
  capture.generatedAt = '';
  capture.analysis.availability.evidence.capturedAt = '';
  for (const facet of [capture.analysis.observation.ordinary, capture.analysis.observation.authorization]) {
    facet.before.capturedAt = '';
    facet.after.capturedAt = '';
  }
  return capture;
}

async function impactCalls(request: APIRequestContext, origin: string) {
  const response = await request.get(`${origin}/metrics`);
  expect(response.status()).toBe(200);
  const metrics = await response.text();
  const count = metrics.match(/kubeatlas_api_v1_requests_total\{endpoint="impact\/\{namespace\}\/\{kind\}\/\{name\}"\} (\d+)/)?.[1];
  expect(count, 'the production impact endpoint counter must exist after a query').toBeDefined();
  return Number(count);
}

async function download(page: Page, format: 'JSON' | 'HTML') {
  const event = page.waitForEvent('download');
  await page.getByRole('button', { name: `Download ${format}`, exact: true }).click();
  const result = await event;
  expect(await result.failure()).toBeNull();
  const path = await result.path();
  expect(path).not.toBeNull();
  return readFile(path!, 'utf8');
}

async function embeddedJSON(page: Page, html: string) {
  return page.evaluate((source) => {
    const doc = new DOMParser().parseFromString(source, 'text/html');
    if (doc.querySelector('script,iframe,object,embed,link,img,form,[src],[href]')) throw new Error('Active/external report content');
    if (!doc.querySelector('meta[http-equiv="Content-Security-Policy"]')?.getAttribute('content')?.includes("default-src 'none'")) throw new Error('Missing report CSP');
    return doc.getElementById('captured-json')?.textContent;
  }, html);
}

async function selectRoot(page: Page, entry: Entry, origin: string) {
  if (!entry.cluster) {
    await page.goto(`${origin}/resources/${entry.namespace}/${entry.kind}/${entry.rootName}`);
    await expect(page.getByTestId('resource-detail-header')).toContainText(`${entry.kind}/${entry.rootName}`);
  } else {
    await page.setExtraHTTPHeaders({ Authorization: `Bearer ${token}` });
    await page.goto(`${origin}/topology`);
    await expect(page.getByRole('button', { name: 'Focus cluster west' })).toHaveCount(0);
    const graphReply = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/federation/graph');
    await page.getByRole('button', { name: `Focus cluster ${entry.cluster}`, exact: true }).click();
    const graph = await (await graphReply).json() as { nodes: Array<{ id: string }> };
    expect(graph.nodes.every((n) => n.id.startsWith('east:'))).toBe(true);
    const index = graph.nodes.findIndex((n) => n.id === `${entry.cluster}:${entry.namespace}/${entry.kind}/${entry.rootName}`);
    expect(index).toBeGreaterThanOrEqual(0);
    const canvas = page.getByTestId('topology-canvas');
    await expect(canvas.locator('canvas')).toHaveCount(3);
    await canvas.focus();
    for (let n = 0; n <= index; n++) await canvas.press('ArrowRight');
    await canvas.press('Enter');
  }
  const response = page.waitForResponse((r) => {
    const url = new URL(r.url());
    return url.pathname === `/api/v1/impact/${entry.namespace}/${entry.kind}/${entry.rootName}` &&
      url.searchParams.get('relation') === entry.relation && url.searchParams.get('max_depth') === String(entry.depth) &&
      (url.searchParams.get('cluster') || '') === entry.cluster;
  }).then(async (received) => {
    // Read eagerly instead of retaining a Response across later UI updates.
    expect(received.status()).toBe(200);
    return received.text();
  });
  await page.getByRole('button', { name: /Analyze impact/ }).click();
  if (entry.depth !== 5) {
    await page.getByRole('combobox', { name: /^Maximum depth / }).click();
    await page.getByRole('option', { name: String(entry.depth), exact: true }).click();
  }
  if (entry.relation === 'dependencies') {
    await page.getByRole('combobox', { name: /^Relation / }).click();
    await page.getByRole('option', { name: 'Dependencies — resources this depends on', exact: true }).click();
  }
  return response;
}

for (const entry of matrix.cases) {
  test(`${entry.name}: real HTTP -> production Web downloads -> actual CLI reports`, async ({ page, request }, info) => {
    const origin = entry.cluster ? federated : local;
    const raw = await selectRoot(page, entry, origin);
    const data = JSON.parse(raw) as ImpactResponse;
    expect(data.analysis.resources.map((m) => m.resource.id).sort()).toEqual(entry.ordinary);
    expect(data.analysis.authorization.resources.map((m) => m.resource.id).sort()).toEqual(entry.authorization);
    expect(data.analysis.truncated).toBe(entry.truncated);
    await expect(page.getByText(`${entry.truncated ? 'At least ' : ''}${entry.ordinary.length} observed · ${entry.direct} direct · ${entry.ordinary.length - entry.direct} indirect`, { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Download JSON' })).toBeDisabled();
    await page.getByRole('checkbox', { name: 'I understand this file contains sensitive cluster topology.' }).check();
    const before = await impactCalls(request, origin);
    expect(await download(page, 'JSON')).toBe(raw);
    expect(await embeddedJSON(page, await download(page, 'HTML'))).toBe(raw);
    expect(await impactCalls(request, origin)).toBe(before);
    for (const format of ['json', 'html'] as const) {
      const output = info.outputPath(`actual-cli.${format}`);
      const args = ['impact', entry.kind, entry.rootName, '-n', entry.namespace, '--server', origin,
        '--relation', entry.relation, '--max-depth', String(entry.depth), '--limit', '200',
        '--expected-uid', entry.uid, '--output', format, '--out', output];
      if (entry.cluster) args.push('--cluster', entry.cluster);
      const result = await exec(cli, args, { env: { ...process.env, KUBEATLAS_TOKEN: entry.cluster ? token : '', KUBECONFIG: info.outputPath('absent-kubeconfig') }, timeout: 10_000 });
      expect(result.stdout).toBe('');
      expect(result.stderr).toContain('Wrote impact report');
      expect((await stat(output)).mode & 0o777).toBe(0o600);
      const content = await readFile(output, 'utf8');
      const json = format === 'json' ? content : await embeddedJSON(page, content);
      expect(json).toBeDefined();
      expect(withoutCaptureClocks(json!)).toEqual(withoutCaptureClocks(raw));
      for (const privateText of [token, 'synthetic-matrix-payload-canary', 'west:']) expect(content).not.toContain(privateText);
    }
    expect(await impactCalls(request, origin)).toBe(before + 2);
  });
}

test('real server authentication denial hides the old Web capture and CLI creates no report', async ({ page, request }, info) => {
  const entry = matrix.cases.find((item) => item.cluster === 'east')!;
  await selectRoot(page, entry, federated);
  await expect(page.getByText('Cluster: east · All namespaces', { exact: true })).toBeVisible();
  await page.getByRole('checkbox', { name: 'I understand this file contains sensitive cluster topology.' }).check();
  await page.setExtraHTTPHeaders({ Authorization: 'Bearer synthetic-denied' });
  await page.getByRole('button', { name: 'Refresh analysis' }).click();
  await expect(page.getByText('Access to this cluster is not authorized.', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Download JSON' })).toHaveCount(0);
  await expect(page.getByText('Cluster: east · All namespaces', { exact: true })).toHaveCount(0);
  for (const [auth, cluster, status] of [['', 'east', 401], ['synthetic-denied', 'east', 403], [token, 'west', 403]] as const) {
    const denied = { ...entry, cluster };
    const response = await request.get(`${federated}${query(denied)}`, { headers: auth ? { Authorization: `Bearer ${auth}` } : {} });
    expect(response.status()).toBe(status);
    const body = await response.text();
    for (const privateText of ['settings', 'west:', entry.uid, 'synthetic-matrix-payload-canary', '"analysis"']) expect(body).not.toContain(privateText);
    const output = info.outputPath(`denied-${status}-${cluster}.json`);
    await expect(exec(cli, ['impact', entry.kind, entry.rootName, '-n', entry.namespace, '--server', federated,
      '--cluster', cluster, '--output', 'json', '--out', output],
    { env: { ...process.env, KUBEATLAS_TOKEN: auth, KUBECONFIG: info.outputPath('absent-kubeconfig') }, timeout: 10_000 })).rejects.toMatchObject({ code: 1, stdout: '' });
    await expect(access(output)).rejects.toMatchObject({ code: 'ENOENT' });
  }
});

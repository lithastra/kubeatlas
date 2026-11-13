import { execFile } from 'node:child_process';
import { access, readFile, stat } from 'node:fs/promises';
import { isIPv4 } from 'node:net';
import { isAbsolute } from 'node:path';
import { promisify } from 'node:util';
import { expect, test } from '@playwright/test';

import type { ImpactResponse, ImpactStep } from '../../src/api/impactTypes';
import { download, embeddedJSON, observeImpactResponse } from './impact-capture-helpers';

const exec = promisify(execFile);

function requiredEnv(name: string) {
  const key = `KUBEATLAS_E2E_IMPACT_${name}`;
  const value = process.env[key];
  if (!value?.trim()) throw new Error(`${key} is required for the deployed impact test`);
  return value;
}

function deployedOrigin() {
  const origin = new URL(requiredEnv('URL'));
  const loopbackHTTP = origin.protocol === 'http:' &&
    ((isIPv4(origin.hostname) && origin.hostname.startsWith('127.')) || origin.hostname === '[::1]');
  if ((!loopbackHTTP && origin.protocol !== 'https:') || origin.username || origin.password ||
      origin.pathname !== '/' || origin.search || origin.hash) {
    throw new Error('KUBEATLAS_E2E_IMPACT_URL must be an HTTPS or literal-loopback HTTP origin');
  }
  return origin.origin;
}

// The caller creates an isolated ConfigMap and Deployment referencing it via
// envFrom and supplies their actual Kubernetes UIDs. This covers standalone
// deployments. Member visibility and external authentication need their own setup.
test('deployed standalone impact resolves and exports the observed ConfigMap reference', async ({ page, request }, info) => {
  const origin = deployedOrigin();
  const cli = requiredEnv('CLI');
  if (!isAbsolute(cli)) throw new Error('KUBEATLAS_E2E_IMPACT_CLI must be an absolute path');

  const namespace = requiredEnv('NAMESPACE');
  const configMap = requiredEnv('CONFIGMAP');
  const configMapUID = requiredEnv('CONFIGMAP_UID');
  const deployment = requiredEnv('DEPLOYMENT');
  const deploymentUID = requiredEnv('DEPLOYMENT_UID');
  const fieldPath = process.env.KUBEATLAS_E2E_IMPACT_FIELD_PATH ??
    '/spec/template/spec/containers/0/envFrom/0/configMapRef';
  const rootId = `${namespace}/ConfigMap/${configMap}`;
  const consumerId = `${namespace}/Deployment/${deployment}`;
  const resourcePath = `${encodeURIComponent(namespace)}/ConfigMap/${encodeURIComponent(configMap)}`;
  const impactPath = `/api/v1/impact/${resourcePath}`;
  const relation = 'dependents';
  const depth = 5;
  const limit = 200;
  const edge = { from: consumerId, to: rootId, type: 'USES_CONFIGMAP' };
  const step: ImpactStep = { ...edge, traversalFrom: rootId, traversalTo: consumerId };

  // The process can be ready while the newly seeded watch objects are still
  // arriving. Wait for the expected detail and edge before the single capture.
  await expect.poll(async () => {
    const response = await request.get(`${origin}/api/v1/resources/${resourcePath}`, { maxRedirects: 0, timeout: 5_000 });
    if (!response.ok()) return `HTTP ${response.status()}`;
    const detail = await response.json() as { resource: { uid?: string }; incoming: ImpactStep[] | null };
    return detail.resource.uid === configMapUID && detail.incoming?.some(
      (item) => item.from === edge.from && item.to === edge.to && item.type === edge.type,
    ) === true;
  }, { timeout: 20_000, message: 'the deployed watch materialized the fixture root and edge' }).toBe(true);

  function assertCapture(raw: string) {
    const capture = JSON.parse(raw) as ImpactResponse;
    expect(capture.schemaVersion).toBe('1');
    expect(capture.scope).toEqual({ clusterId: '', mode: 'single', namespaceScope: 'all_namespaces_in_selected_cluster' });
    const analysis = capture.analysis;
    expect(analysis.root).toMatchObject({ id: rootId, uid: configMapUID, referenceOnly: false });
    expect(analysis).toMatchObject({ relation, maxDepth: depth, limit, truncated: false });
    expect(analysis.availability?.currentGraphRead).toBe('succeeded');
    const consumer = analysis.resources.find((match) => match.resource.id === consumerId);
    expect(consumer).toMatchObject({ resource: { uid: deploymentUID }, depth: 1, path: [step] });
    expect(consumer?.resource.resourceVersion).toBeTruthy();
    expect(analysis.referenceEvidence).toContainEqual(expect.objectContaining({
      ...edge,
      sourceUID: deploymentUID,
      sourceResourceVersion: consumer?.resource.resourceVersion,
      status: 'version_bound',
      uses: [{ fieldPath, mode: 'environment', optional: 'required', refresh: 'container_restart_required' }],
    }));
  }

  const pageErrors: string[] = [];
  page.on('pageerror', (error) => pageErrors.push(error.message));
  await page.goto(`${origin}/resources/${resourcePath}`);
  const serverInfo = await (await request.get(`${origin}/api/v1/info`)).json() as { version: string };
  await expect(page.getByTestId('server-version')).toHaveText(serverInfo.version);
  await expect(page.getByTestId('resource-detail-header')).toContainText(`ConfigMap/${configMap}`);
  const capture = await observeImpactResponse(page, { origin, pathname: impactPath, relation, depth, cluster: '' });
  await page.getByRole('button', { name: 'Analyze impact', exact: true }).click();
  await expect.poll(() => capture() !== undefined, { message: 'the browser received the native deployed impact response' }).toBe(true);
  const response = capture()!;
  expect(response.status).toBe(200);
  expect(new URL(response.url).searchParams.get('expected_uid')).toBe(configMapUID);
  assertCapture(response.raw);

  await page.getByText(/^Reference field evidence \(/).click();
  await expect(page.getByText(`${consumerId} → ${rootId} · USES_CONFIGMAP · version_bound`, { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Download JSON', exact: true })).toBeDisabled();
  await page.getByRole('checkbox', { name: 'I understand this file contains sensitive cluster topology.' }).check();
  expect(await download(page, 'JSON')).toBe(response.raw);
  expect(await embeddedJSON(page, await download(page, 'HTML'))).toBe(response.raw);

  const args = ['impact', 'ConfigMap', configMap, '-n', namespace, '--server', origin,
    '--relation', relation, '--max-depth', String(depth), '--limit', String(limit)];
  const options = {
    env: { ...process.env, KUBEATLAS_TOKEN: '', KUBECONFIG: info.outputPath('absent-kubeconfig') },
    timeout: 10_000,
  };
  for (const format of ['json', 'html'] as const) {
    const output = info.outputPath(`cli.${format}`);
    const result = await exec(cli, [...args, '--expected-uid', configMapUID, '--output', format, '--out', output], options);
    expect(result.stdout).toBe('');
    expect(result.stderr).toContain('Wrote impact report');
    expect((await stat(output)).mode & 0o777).toBe(0o600);
    const report = await readFile(output, 'utf8');
    const raw = format === 'json' ? report : await embeddedJSON(page, report);
    expect(typeof raw).toBe('string');
    // The independent requests can observe different versions and clocks.
    // Each report must establish the same fixture relationship independently.
    assertCapture(raw!);
  }

  const mismatchedUID = `${configMapUID}-mismatch`;
  const query = new URLSearchParams({ relation, max_depth: String(depth), limit: String(limit), expected_uid: mismatchedUID });
  const conflict = await request.get(`${origin}${impactPath}?${query}`, { maxRedirects: 0 });
  expect(conflict.status()).toBe(409);
  expect(await conflict.json()).toEqual({ code: 'conflict', error: 'expected UID cannot be matched to the observed resource instance' });
  const rejected = info.outputPath('rejected.json');
  await expect(exec(cli, [...args, '--expected-uid', mismatchedUID, '--output', 'json', '--out', rejected], options))
    .rejects.toMatchObject({ code: 1, stdout: '', stderr: expect.stringContaining('impact HTTP 409') });
  await expect(access(rejected)).rejects.toMatchObject({ code: 'ENOENT' });
  expect(pageErrors).toEqual([]);
});

import { impactFixture } from '../test/impactFixture';
import { fetchImpactCapture, impactCaptureJSON, impactMaxBytes, isImpactCapture, parseImpactCapture } from './impactCapture';

test('preserves the entire original wire text, large integers and timestamp precision in an immutable capture', () => {
  const raw = JSON.stringify(impactFixture(), null, 2).replace('604800000000000', '9223372036854775807').replaceAll('2026-10-01T00:00:00Z', '2026-10-01T00:00:00.123456789Z');
  const capture = parseImpactCapture(raw);
  expect(impactCaptureJSON(capture)).toBe(raw);
  expect(impactCaptureJSON(capture)).toContain('widgets'); // Full inventory absent from the display-only type.
  expect(isImpactCapture(capture)).toBe(true);
  expect(isImpactCapture({ ...capture })).toBe(false);
  expect(Object.isFrozen(capture.analysis.resources[0].resource)).toBe(true);
  expect(() => { capture.analysis.root.name = 'changed'; }).toThrow();
});

test.each(['raw', 'data', 'stringData', '__proto__'])('rejects unmodeled %s payloads at any known object', (key) => {
  const fixture = impactFixture();
  const raw = JSON.stringify(fixture);
  const bad = raw.replace('"kind":"ConfigMap"', `"kind":"ConfigMap",${JSON.stringify(key)}:{"test":"synthetic-private-canary"}`);
  expect(() => parseImpactCapture(bad)).toThrow('Unsupported impact response schema');
});

test('rejects duplicate and escaped duplicate keys so shadowed payloads cannot reach a raw export', () => {
  const raw = JSON.stringify(impactFixture());
  for (const duplicate of ['"analysis"', '"analysi\\u0073"']) {
    const bad = raw.replace('"analysis":', `${duplicate}:{"raw":{"data":"synthetic-canary"}},"analysis":`);
    expect(() => parseImpactCapture(bad)).toThrow('Duplicate impact response field');
  }
  expect(() => parseImpactCapture(raw.replace('"uid":"fixture-uid"', '"uid":"hidden","u\\u0069d":"fixture-uid"'))).toThrow('Duplicate');
});

test('rejects incomplete, oversized and foreign-scope evidence without registering a capture', () => {
  expect(() => parseImpactCapture('null')).toThrow();
  expect(() => parseImpactCapture(JSON.stringify({ ...impactFixture(), analysis: null }))).toThrow();
  const raw = JSON.stringify(impactFixture('east'));
  expect(() => parseImpactCapture(raw.replace('"clusterId":"east"', '"clusterId":"west"'))).toThrow('scope');
  expect(() => parseImpactCapture(raw.replace('"from":"east:demo/Pod/api"', '"from":"west:demo/Pod/api"'))).toThrow('path');
  expect(() => parseImpactCapture(raw.replace('"direct":1', '"direct":9'))).toThrow('counts');
  expect(() => parseImpactCapture(' '.repeat(impactMaxBytes) + raw)).toThrow('size');
});

test('authorization bounds must match the ordinary query bounds', () => {
  for (const key of ['maxDepth', 'limit'] as const) {
    const fixture = impactFixture();
    fixture.analysis.authorization[key] += 1;
    expect(() => parseImpactCapture(JSON.stringify(fixture))).toThrow('Inconsistent impact bounds');
  }
});

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

function responseFor(chunks: Uint8Array[], status = 200, contentType = 'application/json', cancel = jest.fn()): Response {
  return { status, headers: new Map([['content-type', contentType]]), body: new ReadableStream({
    start(controller) { chunks.forEach((chunk) => controller.enqueue(chunk)); controller.close(); }, cancel,
  }) } as unknown as Response;
}

test('one bounded same-origin GET preserves UTF-8 across chunks and disables redirects/cache', async () => {
  const raw = JSON.stringify(impactFixture()).replace('1.7.0-dev', '1.7.0-dev-合成');
  const bytes = new TextEncoder().encode(raw);
  globalThis.fetch = jest.fn().mockResolvedValue(responseFor(Array.from(bytes, (byte) => new Uint8Array([byte]))));
  const response = await fetchImpactCapture('/api/v1/impact/demo/ConfigMap/settings');
  expect(impactCaptureJSON(response)).toBe(raw);
  expect(globalThis.fetch).toHaveBeenCalledTimes(1);
  expect(globalThis.fetch).toHaveBeenCalledWith(expect.any(String), expect.objectContaining({ method: 'GET', redirect: 'error', cache: 'no-store', credentials: 'same-origin' }));
});

test.each([401, 403, 409, 413, 429, 503, 504, 302])('HTTP %s never reads or repeats an error body', async (status) => {
  const body = { getReader: jest.fn() };
  globalThis.fetch = jest.fn().mockResolvedValue({ status, body });
  await expect(fetchImpactCapture('/api/v1/impact/demo/ConfigMap/settings')).rejects.toMatchObject({ status, message: 'Impact request failed' });
  expect(body.getReader).not.toHaveBeenCalled();
  expect(globalThis.fetch).toHaveBeenCalledTimes(1);
});

test('response limits, invalid encoding and wrong content types fail closed', async () => {
  for (const response of [responseFor([new Uint8Array(impactMaxBytes + 1)]), responseFor([new Uint8Array([0xff])]), responseFor([], 200, 'text/html')]) {
    globalThis.fetch = jest.fn().mockResolvedValue(response);
    await expect(fetchImpactCapture('/api/v1/impact/demo/ConfigMap/settings')).rejects.toThrow();
  }
});

test('an already-aborted selection is aborted before the request', async () => {
  const controller = new AbortController();
  controller.abort();
  globalThis.fetch = jest.fn().mockImplementation(async (_, options: RequestInit) => {
    expect(options.signal?.aborted).toBe(true);
    throw new Error('aborted');
  });
  await expect(fetchImpactCapture('/api/v1/impact/demo/ConfigMap/settings', { signal: controller.signal })).rejects.toThrow();
});

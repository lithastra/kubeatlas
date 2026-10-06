const assert = require('node:assert/strict');
const path = require('node:path');
const { test } = require('node:test');
const { pathToFileURL } = require('node:url');
const tsJest = require('ts-jest').default;
const { createTransformer, normalizeResult } = require('./ts-jest-transformer.cjs');

const rootDir = path.resolve(__dirname, '..');
const sourcePath = path.join(rootDir, 'src', 'fixture with spaces', 'source #%.ts');
const source = 'export const value: number = 42;';
const map = {
  version: 3,
  file: sourcePath,
  sources: [pathToFileURL(sourcePath).href],
  sourcesContent: [source],
  names: ['value'],
  mappings: 'AAAA',
};

function inlineResult(sourceMap) {
  return {
    code: source + '\n//# sourceMappingURL=data:application/json;charset=utf-8;base64,' +
      Buffer.from(JSON.stringify(sourceMap)).toString('base64'),
  };
}

function readMap(result) {
  return JSON.parse(Buffer.from(result.code.split('base64,').pop(), 'base64').toString('utf8'));
}

test('local file URLs become relative paths without changing mappings or source content', () => {
  const result = normalizeResult(inlineResult(map), sourcePath);
  assert.deepEqual(readMap(result), { ...map, sources: ['source #%.ts'] });
  assert.ok(result.code.startsWith(source + '\n'));
  assert.deepEqual(map.sources, [pathToFileURL(sourcePath).href]);
});

test('nested and sibling source paths use portable separators', () => {
  const sibling = path.join(rootDir, 'src', 'shared', 'value.ts');
  const result = normalizeResult(inlineResult({ ...map, sources: [pathToFileURL(sibling).href] }), sourcePath);
  assert.deepEqual(readMap(result).sources, ['../shared/value.ts']);
});

test('absolute filesystem sources become relative paths on both map representations', () => {
  const sibling = path.join(rootDir, 'src', 'shared', 'value.ts');
  const sourceMap = { ...map, sources: [sourcePath, sibling] };
  const expected = { ...sourceMap, sources: ['source #%.ts', '../shared/value.ts'] };
  assert.deepEqual(readMap(normalizeResult(inlineResult(sourceMap), sourcePath)), expected);
  for (const value of [sourceMap, JSON.stringify(sourceMap)]) {
    assert.deepEqual(normalizeResult({ code: source, map: value }, sourcePath).map, expected);
  }
  assert.deepEqual(sourceMap.sources, [sourcePath, sibling]);
});

test('existing relative paths and non-file URLs are preserved', () => {
  const sources = ['source.ts', '../shared/value.ts', 'https://example.invalid/source.ts'];
  assert.deepEqual(readMap(normalizeResult(inlineResult({ ...map, sources }), sourcePath)).sources, sources);
});

test('both object and string source maps are normalized', () => {
  for (const sourceMap of [map, JSON.stringify(map)]) {
    const result = normalizeResult({ code: source, map: sourceMap }, sourcePath);
    assert.deepEqual(result.map, { ...map, sources: ['source #%.ts'] });
  }
});

test('code without a source map is unchanged', () => {
  const result = { code: source, map: null };
  assert.deepEqual(normalizeResult(result, sourcePath), result);
});

test('source-map-like text inside executable code is not rewritten', () => {
  const comment = inlineResult(map).code.split('\n').pop();
  for (const code of [
    'const text = ' + JSON.stringify(comment) + ';',
    'const text = `\n' + comment + '\n`;',
  ]) {
    assert.deepEqual(normalizeResult({ code }, sourcePath), { code });
    const withMap = inlineResult(map);
    withMap.code = code + '\n' + withMap.code;
    assert.ok(normalizeResult(withMap, sourcePath).code.startsWith(code + '\n'));
  }
});

test('CRLF output keeps its source mapping and line endings', () => {
  const result = inlineResult(map);
  result.code = result.code.replaceAll('\n', '\r\n') + '\r\n';
  const normalized = normalizeResult(result, sourcePath);
  assert.deepEqual(readMap(normalized).sources, ['source #%.ts']);
  assert.ok(normalized.code.endsWith('\r\n'));
});

test('invalid source maps fail instead of silently discarding coverage', () => {
  assert.throws(() => normalizeResult({ code: source, map: '{invalid' }, sourcePath), SyntaxError);
});

const config = { rootDir, cwd: rootDir, globals: {} };
const transformOptions = {
  config,
  configString: JSON.stringify(config),
  cacheFS: new Map([[sourcePath, source]]),
  instrument: false,
  supportsDynamicImport: false,
  supportsExportNamespaceFrom: false,
  supportsStaticESM: false,
  supportsTopLevelAwait: false,
};
const transformerOptions = {
  tsconfig: { target: 'ES2022', module: 'CommonJS', isolatedModules: true },
};

test('the real synchronous and asynchronous ts-jest transforms preserve executable code', async () => {
  const original = tsJest.createTransformer(transformerOptions).process(source, sourcePath, transformOptions);
  const transformer = createTransformer(transformerOptions);
  for (const result of [
    transformer.process(source, sourcePath, transformOptions),
    await transformer.processAsync(source, sourcePath, transformOptions),
  ]) {
    const before = readMap(original);
    const after = readMap(result);
    assert.deepEqual(after.sources, ['source #%.ts']);
    assert.equal(after.mappings, before.mappings);
    assert.deepEqual(after.sourcesContent, before.sourcesContent);
    assert.equal(result.code.split('//# sourceMappingURL=')[0], original.code.split('//# sourceMappingURL=')[0]);
  }
});

test('the adapter has distinct stable cache keys on both transformer paths', async () => {
  const transformer = createTransformer(transformerOptions);
  const key = transformer.getCacheKey(source, sourcePath, transformOptions);
  const upstreamKey = tsJest.createTransformer(transformerOptions).getCacheKey(source, sourcePath, transformOptions);
  assert.notEqual(key, upstreamKey);
  assert.equal(key, transformer.getCacheKey(source, sourcePath, transformOptions));
  assert.equal(key, await transformer.getCacheKeyAsync(source, sourcePath, transformOptions));
});

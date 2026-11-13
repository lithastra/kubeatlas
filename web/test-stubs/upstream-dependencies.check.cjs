// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
const assert = require('node:assert/strict');
const {mkdtemp, readFile, writeFile, rm} = require('node:fs/promises');
const {createRequire} = require('node:module');
const os = require('node:os');
const path = require('node:path');
const {test} = require('node:test');

const fromBabel = createRequire(require.resolve('babel-plugin-istanbul'));
const consumerPath = fromBabel.resolve('@istanbuljs/load-nyc-config');
const fromConsumer = createRequire(consumerPath);

test('every locked minimatch copy is patched and matches the installed tree', async () => {
  const root = path.resolve(__dirname, '..');
  const lock = JSON.parse(await readFile(path.join(root, 'package-lock.json'), 'utf8'));
  const copies = Object.entries(lock.packages).filter(([key]) => key.endsWith('node_modules/minimatch'));
  assert.ok(copies.length > 0, 'expected actual minimatch consumers');
  for (const [key, entry] of copies) {
    assert.doesNotMatch(entry.version, /^(?:[7-9]|10)\.|^11\.0\.0(?:$|-)/,
      `vulnerable minimatch remains at ${key}`);
    const installed = JSON.parse(await readFile(path.join(root, key, 'package.json'), 'utf8'));
    assert.equal(installed.version, entry.version, `minimatch installation differs at ${key}`);
  }
});

// Read the packages that the real consumer resolves, not an unrelated hoisted
// copy. A changed lockfile does not update an existing node_modules tree.
async function assertLockedParser() {
  const root = path.resolve(__dirname, '..');
  const manifest = JSON.parse(await readFile(path.join(root, 'package.json'), 'utf8'));
  const lock = JSON.parse(await readFile(path.join(root, 'package-lock.json'), 'utf8'));
  const verify = async (resolver, name) => {
    const file = resolver.resolve(`${name}/package.json`);
    const installed = JSON.parse(await readFile(file, 'utf8'));
    const key = path.relative(root, path.dirname(file)).split(path.sep).join('/');
    const locked = lock.packages[key];
    assert.ok(locked, `${name} resolves outside the locked dependency tree: ${key}`);
    assert.equal(installed.version, locked.version,
      `${name} runtime version differs from package-lock.json at ${key}; reconcile installed dependencies without weakening this check`);
    return {installed, file};
  };
  const consumer = await verify(fromBabel, '@istanbuljs/load-nyc-config');
  const parser = await verify(fromConsumer, 'js-yaml');
  const override = manifest.overrides[`@istanbuljs/load-nyc-config@${consumer.installed.version}`]?.['js-yaml'];
  assert.ok(override, 'the Istanbul YAML override must remain explicit');
  assert.equal(parser.installed.version, override, 'Istanbul must resolve the exact overridden YAML parser');
  await verify(createRequire(parser.file), 'argparse');
}

test('Istanbul loads YAML coverage config with locked dependencies, extensions and typed values', async () => {
  await assertLockedParser();
  const {loadNycConfig} = fromBabel('@istanbuljs/load-nyc-config');
  const dir = await mkdtemp(path.join(os.tmpdir(), 'nyc-consumer-'));
  try {
    await writeFile(path.join(dir, 'package.json'), JSON.stringify({nyc: {reporter: ['lcov']}}));
    await writeFile(path.join(dir, 'base.yml'), 'all: true\nexclude: ["fixtures/**"]\n');
    await writeFile(path.join(dir, '.nycrc.yml'), [
      'extends: ./base.yml',
      'include: ["src/**/*.ts"]',
      'extension: .ts',
      'check-coverage: true',
      'lines: 60',
      'report-dir: coverage',
      '',
    ].join('\n'));
    const config = await loadNycConfig({cwd: dir});
    assert.equal(config.cwd, dir);
    assert.deepEqual(config.reporter, ['lcov']);
    assert.equal(config.all, true);
    assert.deepEqual(config.exclude, ['fixtures/**']);
    assert.deepEqual(config.include, ['src/**/*.ts']);
    assert.deepEqual(config.extension, ['.ts']);
    assert.equal(config.checkCoverage, true);
    assert.equal(config.lines, 60);
    assert.equal(config.reportDir, 'coverage');
    await writeFile(path.join(dir, '.nycrc.yml'), 'include: [unterminated\n');
    await assert.rejects(loadNycConfig({cwd: dir}), {name: 'YAMLException'});
    await writeFile(path.join(dir, '.nycrc.yml'), 'include: !!js/function "function() { return true; }"\n');
    await assert.rejects(loadNycConfig({cwd: dir}), {name: 'YAMLException'});
  } finally {
    await rm(dir, {recursive: true, force: true});
  }
});

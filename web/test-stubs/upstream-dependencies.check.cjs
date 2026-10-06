// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
const assert = require('node:assert/strict');
const {mkdtemp, writeFile, rm} = require('node:fs/promises');
const {createRequire} = require('node:module');
const os = require('node:os');
const path = require('node:path');
const {test} = require('node:test');

const fromBabel = createRequire(require.resolve('babel-plugin-istanbul'));
const {loadNycConfig} = fromBabel('@istanbuljs/load-nyc-config');

test('Istanbul loads YAML coverage config with extensions and typed values', async () => {
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

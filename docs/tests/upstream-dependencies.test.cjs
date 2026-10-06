// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const path = require('node:path');
const {test} = require('node:test');
const {pathToFileURL} = require('node:url');

const fromCore = createRequire(require.resolve('@docusaurus/core/package.json'));
const fromBundler = createRequire(require.resolve('@docusaurus/bundler'));

test('Docusaurus worker options retain task results and ignore inherited filenames', async () => {
  const {default: Tinypool} = await import(pathToFileURL(fromCore.resolve('tinypool')).href);
  const pool = new Tinypool({
    filename: pathToFileURL(path.join(__dirname, 'fixtures/ssg-worker.mjs')).href,
    minThreads: 1,
    maxThreads: 2,
    concurrentTasksPerWorker: 1,
    runtime: 'worker_threads',
    isolateWorkers: false,
    workerData: {marker: 'ssg-fixture'},
    maxMemoryLimitBeforeRecycle: 1000000000,
    resourceLimits: {},
  });
  try {
    assert.deepEqual(await Promise.all([pool.run('first'), pool.run('second')]), [
      {marker: 'ssg-fixture', task: 'first'},
      {marker: 'ssg-fixture', task: 'second'},
    ]);
    const options = Object.create({
      filename: pathToFileURL(path.join(__dirname, 'fixtures/missing-worker.mjs')).href,
    });
    assert.deepEqual(await pool.run('inherited', options), {
      marker: 'ssg-fixture', task: 'inherited',
    });
    assert.equal(Object.hasOwn(options, 'filename'), false);
  } finally {
    await pool.destroy();
  }
});

test('Docusaurus cssnano preset retains selector, calc and media output', async () => {
  const cssnano = fromBundler('cssnano');
  const preset = fromBundler('@docusaurus/cssnano-preset');
  const postcss = fromBundler('postcss');
  const css = '.a, .b { color: rgb(255, 0, 0); margin: calc(1px + 2px) 0px 0px 0px; } ' +
    '@media (min-width: 600px) { .a:hover { color: blue; } }';
  const result = await postcss([cssnano({preset: preset()})]).process(css, {from: undefined});
  assert.equal(result.css,
    '.a,.b{color:red;margin:3px 0 0}@media (min-width:600px){.a:hover{color:blue}}');
  assert.deepEqual(result.warnings(), []);
});

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

const assert = require('node:assert/strict');
const {spawnSync} = require('node:child_process');
const {createRequire} = require('node:module');
const {test} = require('node:test');
const vm = require('node:vm');

// Resolve from the consumers, not a possibly unrelated top-level installation.
const fromBundler = createRequire(require.resolve('@docusaurus/bundler'));
const fromCssMinimizer = createRequire(fromBundler.resolve('css-minimizer-webpack-plugin'));
const fromDevServer = createRequire(require.resolve('webpack-dev-server'));
const fromSockjs = createRequire(fromDevServer.resolve('sockjs'));

for (const consumer of ['copy-webpack-plugin', 'css-minimizer-webpack-plugin']) {
  const fromConsumer = createRequire(fromBundler.resolve(consumer));
  const serialize = fromConsumer('serialize-javascript');

  test(`${consumer}: serialization preserves cache keys and worker values`, () => {
    const value = {
      pattern: /example/gi,
      date: new Date('2026-01-01T00:00:00.000Z'),
      transform: (input) => input.toUpperCase(),
      text: '</script>',
      count: 123n,
    };
    const encoded = serialize(value);
    assert.equal(serialize(value), encoded);
    assert.ok(!encoded.includes('</script>'));
    const decoded = vm.runInNewContext(`(${encoded})`, {}, {timeout: 1000});
    assert.equal(decoded.pattern.source, 'example');
    assert.equal(decoded.pattern.flags, 'gi');
    assert.equal(decoded.date.toISOString(), value.date.toISOString());
    assert.equal(decoded.transform('css'), 'CSS');
    assert.equal(decoded.text, value.text);
    assert.equal(decoded.count, 123n);
  });

  test(`${consumer}: function bodies cannot terminate an HTML script element`, () => {
    // Synthetic source only; do not invoke the function or load HTML in a browser.
    const fn = vm.runInNewContext(
      "(function fixture(x) { return x</script=+/ + '</script>fixture'; })",
      {}, {timeout: 1000});
    const encoded = serialize({fn});
    assert.doesNotMatch(encoded, /<\/script[\t\n\f\r />]/i);
    assert.equal(typeof vm.runInNewContext(`(${encoded})`, {}, {timeout: 1000}).fn,
      'function');
  });

  test(`${consumer}: RegExp flags cannot inject expressions`, () => {
    // Harmless synthetic marker: no commands, network, or filesystem access.
    const marker = '"+(globalThis.injected=true)+"';
    const regexp = Object.create(RegExp.prototype);
    Object.defineProperties(regexp, {
      source: {value: 'example'},
      flags: {value: marker},
      toJSON: {value: () => 'placeholder'},
    });
    const context = {injected: false};
    const encoded = serialize({value: regexp});
    try {
      vm.runInNewContext(`(${encoded})`, context, {timeout: 1000});
    } catch (error) {
      // Escaped, invalid RegExp flags may be rejected without evaluating them.
      assert.equal(error.name, 'SyntaxError');
    }
    assert.equal(context.injected, false);
  });

  test(`${consumer}: forged Date strings are rejected before serialization`, () => {
    const date = new Date('2026-01-01T00:00:00.000Z');
    date.toISOString = () => '"+(globalThis.injected=true)+"';
    assert.throws(() => serialize({date}), {
      name: 'TypeError',
      message: 'Invalid Date ISO string',
    });
  });

  test(`${consumer}: array-like input is bounded`, () => {
    // A child process bounds CPU/memory even if a future dependency regresses.
    const child = spawnSync(process.execPath, ['--max-old-space-size=64', '-e', `
      const assert = require('node:assert/strict');
      const serialize = require(process.argv[1]);
      const value = Object.create(Array.prototype);
      value.length = 4294967295;
      assert.deepEqual(JSON.parse(serialize(value)), {length: 4294967295});
    `, fromConsumer.resolve('serialize-javascript')], {
      timeout: 5000,
      maxBuffer: 64 * 1024,
      encoding: 'utf8',
    });
    assert.ifError(child.error);
    assert.equal(child.status, 0, child.stderr);
  });
}

test('CSS worker consumes serialized minimizer functions and options', async () => {
  const serialize = fromCssMinimizer('serialize-javascript');
  const {transform} = fromBundler('css-minimizer-webpack-plugin/dist/minify.js');
  const result = await transform(serialize({
    name: 'fixture.css',
    input: 'body { color: red; }',
    minimizer: {
      implementation: async (input, _map, options) => ({
        code: Object.values(input)[0].replace(options.whitespace, ''),
      }),
      options: {whitespace: /\s+/g},
    },
  }));
  assert.deepEqual(result.errors, []);
  assert.equal(result.outputs[0].code, 'body{color:red;}');
});

test('SockJS retains its CommonJS UUID v4 connection contract', () => {
  const uuid = fromSockjs('uuid');
  const {SockJSConnection} = fromDevServer('sockjs/lib/transport.js');
  const sent = [];
  const session = {
    prefix: '/socket',
    readyState: 1,
    send: (value) => sent.push(value),
    close: () => {},
  };
  const first = new SockJSConnection(session);
  const second = new SockJSConnection(session);
  assert.ok(uuid.validate(first.id));
  assert.equal(uuid.version(first.id), 4);
  assert.notEqual(first.id, second.id);
  first.write('message');
  assert.deepEqual(sent, ['message']);
  first.close();
  second.close();
});

test('SockJS UUID dependency rejects undersized v3/v5 output buffers', () => {
  const uuid = fromSockjs('uuid');
  for (const name of ['v3', 'v5']) {
    assert.throws(() => uuid[name]('example', uuid[name].DNS, new Uint8Array(15)),
      /buffer|bounds|length/i);
  }
});

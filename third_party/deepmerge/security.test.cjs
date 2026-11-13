const assert = require('node:assert/strict');
const { test } = require('node:test');
const { createRequire } = require('node:module');
const path = require('node:path');

if (['web', 'docs'].includes(path.basename(process.cwd()))) {
  test('the installed consumer resolves the maintained package', () => {
    const consumerRequire = createRequire(path.join(process.cwd(), 'package.json'));
    assert.equal(consumerRequire('deepmerge'), require('./dist/cjs.js'));
    const lock = consumerRequire('./package-lock.json');
    for (const [name, entry] of Object.entries(lock.packages)) {
      if (name.endsWith('node_modules/deepmerge')) assert.equal(entry.link, true);
    }
  });
}

for (const entry of ['./dist/cjs.js', './dist/umd.js']) {
  const merge = require(entry);
  test(`${entry} preserves own __proto__ data without changing the result prototype`, () => {
    const malicious = JSON.parse('{"__proto__":{"poisoned":true},"safe":1}');
    for (const result of [merge(malicious, {}), merge(Object.create(null), malicious)]) {
      assert.equal(Object.getPrototypeOf(result), Object.prototype);
      assert.equal(result.poisoned, undefined);
      assert.equal(result.safe, 1);
      assert.deepEqual(Object.getOwnPropertyDescriptor(result, '__proto__'), {
        value: { poisoned: true }, writable: true, enumerable: true, configurable: true,
      });
    }
    assert.equal({}.poisoned, undefined);
  });
  test(`${entry} retains nested merge, arrays, symbols and custom merge behavior`, () => {
    const symbol = Symbol('field');
    const left = { a: { x: 1 }, items: [1], [symbol]: { x: 2 } };
    const right = { a: { y: 3 }, items: [2], [symbol]: { y: 4 } };
    assert.deepEqual(merge(left, right), {
      a: { x: 1, y: 3 }, items: [1, 2], [symbol]: { x: 2, y: 4 },
    });
    assert.deepEqual(merge(left, right, { customMerge: key => key === 'a' ? () => ({ z: 5 }) : undefined }).a, { z: 5 });
    assert.deepEqual(merge.all([{ a: 1 }, { b: 2 }]), { a: 1, b: 2 });
    assert.deepEqual(left.items, [1]);
  });
}

// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0
const assert = require('node:assert/strict');
const {spawnSync} = require('node:child_process');
const {createHash} = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const {createRequire} = require('node:module');
const {test} = require('node:test');

const docs = path.resolve(__dirname, '..');
const baseline = process.env.KUBEATLAS_SECURITY_BASELINE === '1';
const bracesPath = baseline ? require.resolve('braces') : path.join(docs, 'vendor/braces');
const cachePath = baseline ? require.resolve('http-cache-semantics') : path.join(docs, 'vendor/http-cache-semantics');
const braces = require(bracesPath);
const CachePolicy = require(cachePath);
const request = (headers = {}) => ({url: 'https://example.invalid/fixture', method: 'GET', headers: {host: 'example.invalid', ...headers}});
function policy(headers, shared = true, original = request()) {
  const result = new CachePolicy(original, {status: 200, headers}, {shared});
  result.now = () => result._responseTime + 2000;
  return result;
}

function childBraces(mode, inputKind) {
  const child = spawnSync(process.execPath, ['--max-old-space-size=64', '--stack-size=512', '-e', `
    const assert = require('node:assert/strict');
    const braces = require(process.argv[1]);
    const mode = process.argv[2];
    let input;
    if (process.argv[3] === 'ast') {
      input = {type:'root',nodes:[]}; let node=input;
      for(let i=0;i<10000;i++) {const child={type:'paren',nodes:[]};node.nodes.push(child);node=child;}
      node.nodes.push({type:'text',value:'x'});
    } else {
      const open = process.argv[3] === 'paren' ? '(' : '{';
      const close = open === '(' ? ')' : '}';
      input = open.repeat(4000)+'x'+close.repeat(4000);
    }
    assert.throws(() => braces[mode](input), error =>
      error instanceof SyntaxError && /nesting exceeds the local limit/.test(error.message));
  `, bracesPath, mode, inputKind], {timeout: 5000, maxBuffer: 65536, encoding: 'utf8'});
  assert.ifError(child.error);
  assert.equal(child.status, 0, child.stderr);
}
for (const mode of ['parse', 'compile', 'expand', 'stringify']) {
  for (const input of ['brace', 'paren']) {
    test(`attack: nesting ${mode}/${input} rejects before stack exhaustion`, () => childBraces(mode, input));
  }
}
for (const mode of ['compile', 'expand', 'stringify']) {
  test(`attack: supplied AST ${mode} cannot bypass the depth bound`, () => childBraces(mode, 'ast'));
}
test('braces retains ordinary ranges, nested alternatives and escaping', () => {
  assert.deepEqual(braces.expand('file-{01..03}.{js,ts}'), ['file-01.js','file-01.ts','file-02.js','file-02.ts','file-03.js','file-03.ts']);
  assert.deepEqual(braces.expand('{a,{b,c}}'), ['a','b','c']);
  assert.equal(braces.compile('docs/*.{md,mdx}'), 'docs/*.(md|mdx)');
  assert.equal(braces.stringify('docs/{a,b}'), 'docs/{a,b}');
  assert.equal(braces.compile('\\{literal\\}'), '{literal}');
  const shallow = '{'.repeat(127)+'x'+'}'.repeat(127);
  assert.doesNotThrow(() => braces.compile(shallow));
  assert.throws(() => braces.compile('{'.repeat(128)+'x'+'}'.repeat(128)), /nesting exceeds/);
});
test('quoted and escaped literal braces do not consume AST depth', () => {
  assert.doesNotThrow(() => braces.compile('"'+'{'.repeat(1000)+'"'));
  assert.doesNotThrow(() => braces.compile('\\{'.repeat(1000)));
});

for (const maxStale of ['999999', '']) {
  test(`attack: cookie security-zeroed shared cache rejects max-stale=${maxStale}`, () => {
    const p = policy({'cache-control':'max-age=300','set-cookie':'fixture=session'});
    assert.equal(p.maxAge(), 0);
    const result = p.evaluateRequest(request({'cache-control':`max-stale${maxStale ? '='+maxStale : ''}`}));
    assert.equal(result.response, undefined);
    assert.equal(result.revalidation.synchronous, true);
    assert.equal(p.satisfiesWithoutRevalidation(request({'cache-control':'max-stale=999999'})), false);
  });
}
for (const directive of ['private, max-age=300', 'no-store', 'no-cache, max-age=300']) {
  test(`cache refuses max-stale and stale-while-revalidate for ${directive}`, () => {
    const p = policy({'cache-control':directive+', stale-while-revalidate=999999'});
    assert.equal(p.evaluateRequest(request({'cache-control':'max-stale=999999'})).response, undefined);
    assert.equal(p.evaluateRequest(request()).response, undefined);
  });
}
test('authenticated shared response without explicit permission cannot be reused', () => {
  const p = policy({'cache-control':'max-age=300'},true,request({authorization:'synthetic'}));
  assert.equal(p.storable(),false);
  assert.equal(p.evaluateRequest(request({'cache-control':'max-stale=999999'})).response,undefined);
});
test('serialized security-zeroed entries retain the safety guard', () => {
  const original = policy({'cache-control':'max-age=300, stale-while-revalidate=999999','set-cookie':'fixture=session'});
  const p = CachePolicy.fromObject(JSON.parse(JSON.stringify(original.toObject())));
  p.now=()=>p._responseTime+2000;
  assert.equal(p.evaluateRequest(request({'cache-control':'max-stale=999999'})).response,undefined);
});
test('shared s-maxage and proxy-revalidate cannot bypass expired revalidation', () => {
  for(const value of ['s-maxage=1','max-age=1, proxy-revalidate']) {
    const p=policy({'cache-control':value});
    assert.equal(p.evaluateRequest(request({'cache-control':'max-stale=999999'})).response,undefined);
  }
});
test('security-zeroed responses cannot return through origin-error fallback', () => {
  for(const directive of ['max-age=300','private','no-store','no-cache','must-revalidate','s-maxage=0','proxy-revalidate']) {
    const p=policy({'cache-control':directive+', stale-if-error=999999, stale-while-revalidate=999999','set-cookie':'fixture=session'});
    assert.equal(p.useStaleWhileRevalidate(),false);
    const result=p.revalidatedPolicy(request(),{status:503,headers:{}});
    assert.notEqual(result.policy,p);
    assert.equal(result.modified,true);
  }
});
test('ordinary stale-error fallback and asynchronous revalidation remain usable', () => {
  const p=policy({'cache-control':'max-age=1, stale-if-error=60, stale-while-revalidate=60'});
  assert.equal(p.useStaleWhileRevalidate(),true);
  assert.equal(p.revalidatedPolicy(request(),{status:503,headers:{}}).policy,p);
  const result=p.evaluateRequest(request());
  assert.ok(result.response);
  assert.equal(result.revalidation.synchronous,false);
});
test('normal freshness, explicitly permitted cookies and private caches remain usable', () => {
  for(const [headers,shared] of [
    [{'cache-control':'max-age=300'},true],
    [{'cache-control':'public, max-age=300','set-cookie':'fixture=session'},true],
    [{'cache-control':'immutable, max-age=300','set-cookie':'fixture=session'},true],
    [{'cache-control':'private, max-age=300','set-cookie':'fixture=session'},false],
    [{'cache-control':'s-maxage=300'},true],
  ]) {
    assert.equal(policy(headers,shared).satisfiesWithoutRevalidation(request()),true);
  }
  assert.equal(policy({'cache-control':'max-age=1'}).satisfiesWithoutRevalidation(request({'cache-control':'max-stale=10'})),true);
  assert.equal(policy({'cache-control':'max-age=1'}).satisfiesWithoutRevalidation(request()),false);
});
test('Vary and request no-cache still require revalidation', () => {
  const p=policy({'cache-control':'max-age=300','vary':'accept-language'},true,request({'accept-language':'en'}));
  assert.equal(p.satisfiesWithoutRevalidation(request({'accept-language':'ja'})),false);
  assert.equal(p.satisfiesWithoutRevalidation(request({'accept-language':'en','cache-control':'no-cache'})),false);
});

test('every installed transitive vulnerable-package consumer resolves the patched source', () => {
  const lock=JSON.parse(fs.readFileSync(path.join(docs,'package-lock.json'),'utf8'));
  const seen=new Set();
  for(const [entry,value] of Object.entries(lock.packages)) {
    if(!entry.includes('node_modules/') || value.link) continue;
    for(const name of ['braces','http-cache-semantics']) {
      if(!value.dependencies?.[name]) continue;
      const consumer=createRequire(path.join(docs,entry,'package.json'));
      const actual=fs.realpathSync(consumer.resolve(name));
      assert.ok(actual.startsWith(path.join(docs,'vendor',name)+path.sep),`${entry} resolves ${actual}`);
      seen.add(name);
    }
  }
  assert.deepEqual([...seen].sort(),['braces','http-cache-semantics']);
  const fromUtils=createRequire(require.resolve('@docusaurus/utils'));
  const micromatch=fromUtils('micromatch');
  assert.deepEqual(micromatch(['guide.md','guide.mdx','guide.js'],'*.{md,mdx}'),['guide.md','guide.mdx']);
});
test('vendored sources retain upstream identity, licenses and reviewed patched hashes', () => {
  const manifest=JSON.parse(fs.readFileSync(path.join(docs,'vendor/upstream-sources.json'),'utf8'));
  for(const item of manifest) {
    const root=path.join(docs,'vendor',item.name);
    const pkg=JSON.parse(fs.readFileSync(path.join(root,'package.json'),'utf8'));
    assert.equal(pkg.name,item.name);
    assert.equal(pkg.kubeatlasPatch.upstreamVersion,item.upstream_version);
    assert.equal(pkg.kubeatlasPatch.upstreamIntegrity,item.upstream_integrity);
    assert.equal(pkg.private,true);
    assert.ok(item.modified_files.includes(item.name==='braces'?'lib/parse.js':'index.js'));
    assert.deepEqual(Object.keys(item.patched_files_sha256).sort(), Object.keys(item.upstream_files_sha256).sort());
    for(const [file,digest] of Object.entries(item.patched_files_sha256)) {
      assert.equal(createHash('sha256').update(fs.readFileSync(path.join(root,file))).digest('hex'),digest,`${item.name}/${file}`);
    }
    assert.equal(createHash('sha256').update(fs.readFileSync(path.join(root,'LICENSE'))).digest('hex'),item.upstream_files_sha256.LICENSE);
  }
});

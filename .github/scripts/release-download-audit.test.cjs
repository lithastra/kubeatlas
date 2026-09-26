'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { validateInputs, archiveNames, audit, sha256 } = require('./release-download-audit.cjs');
const WEB = 'https://github.com/lithastra/kubeatlas/releases';

function fixture() {
  const inputs = { tag: 'v1.6.0', commit: 'a'.repeat(40), tagObject: 'b'.repeat(40), releaseID: '42' };
  const bodies = new Map(archiveNames(inputs.tag).map(name => [name, Buffer.from(`archive: ${name}\n`)]));
  const checksums = [...bodies].map(([name, bytes]) => `${sha256(bytes).slice(7)}  ${name}`).join('\n') + '\n';
  bodies.set('checksums.txt', Buffer.from(checksums));
  inputs.checksumsDigest = sha256(bodies.get('checksums.txt'));
  const state = {
    inputs, bodies, calls: [], releaseReads: 0, tagReads: 0,
    reference: { object: { type: 'tag', sha: inputs.tagObject } },
    tag: { sha: inputs.tagObject, tag: inputs.tag, object: { type: 'commit', sha: inputs.commit },
      verification: { verified: true, reason: 'valid' } },
    release: { id: 42, tag_name: inputs.tag, draft: false, prerelease: false,
      published_at: '2026-01-01T00:00:00Z', html_url: `${WEB}/tag/${inputs.tag}`,
      assets: [...bodies].map(([name, bytes], i) => ({ name, size: bytes.length, id: 100 + i,
        state: 'uploaded', digest: sha256(bytes), updated_at: '2026-01-01T00:00:00Z',
        browser_download_url: `${WEB}/download/${inputs.tag}/${name}` })) },
  };
  state.fetch = async (url, options) => {
    state.calls.push(url);
    assert.equal(options.method, 'GET');
    assert.equal(options.redirect, 'manual');
    assert.equal(options.credentials, 'omit');
    assert(options.signal instanceof AbortSignal);
    assert.deepEqual(Object.keys(options.headers).sort(), ['Accept', 'Accept-Encoding', 'User-Agent']);
    const target = new URL(url);
    if (target.hostname === 'api.github.com') {
      const route = target.pathname;
      let data;
      if (route.endsWith(`/git/ref/tags/${inputs.tag}`)) data = state.reference;
      else if (route.endsWith(`/git/tags/${inputs.tagObject}`)) {
        state.tagReads++;
        data = state.tagReads > 1 && state.finalTag ? state.finalTag : state.tag;
      } else if (route.endsWith('/releases/42')) {
        state.releaseReads++;
        data = state.releaseReads > 1 && state.finalRelease ? state.finalRelease : state.release;
      } else throw new Error(`Unexpected test API route: ${route}`);
      return new Response(JSON.stringify(data));
    }
    const name = target.pathname.split('/').pop();
    if (state.respond) {
      const result = await state.respond(name, target);
      if (result) return result;
    }
    assert(bodies.has(name));
    return new Response(bodies.get(name));
  };
  state.run = () => audit(inputs, { fetch: state.fetch, auditToolsCommit: 'c'.repeat(40) });
  return state;
}

test('verifies complete anonymous checksum and ten archive bodies, then rechecks identities', async () => {
  const state = fixture();
  const result = await state.run();
  assert.equal(result.status, 'passed');
  assert.equal(result.phase, 'complete');
  assert.equal(result.downloads.length, 11);
  assert.equal(result.totalBytes, [...state.bodies.values()].reduce((sum, bytes) => sum + bytes.length, 0));
  assert.equal(result.auditToolsCommit, 'c'.repeat(40));
  assert.equal(state.releaseReads, 2);
  assert.equal(state.tagReads, 2);
  assert.equal(result.requests.length, 17);
  assert(result.requests.every(request => request.status === 'complete' && request.httpStatus === 200));
});

for (const [name, change, error] of [
  ['draft', s => { s.release.draft = true; }, 'release_identity'],
  ['unpublished', s => { s.release.published_at = null; }, 'release_identity'],
  ['prerelease', s => { s.release.prerelease = true; }, 'release_identity'],
  ['wrong Release ID', s => { s.release.id++; }, 'release_identity'],
  ['wrong Release tag', s => { s.release.tag_name = 'v1.5.2'; }, 'release_identity'],
  ['invalid publication date', s => { s.release.published_at = 'invalid'; }, 'release_identity'],
  ['lightweight tag', s => { s.reference.object.type = 'commit'; }, 'tag_object_drift'],
  ['moved tag', s => { s.reference.object.sha = 'd'.repeat(40); }, 'tag_object_drift'],
  ['wrong tag commit', s => { s.tag.object.sha = 'd'.repeat(40); }, 'tag_signature_or_commit'],
  ['unsigned tag', s => { s.tag.verification.verified = false; }, 'tag_signature_or_commit'],
  ['expired signature', s => { s.tag.verification.reason = 'expired_key'; }, 'tag_signature_or_commit'],
  ['missing archive', s => { s.release.assets.pop(); }, 'asset_inventory'],
  ['extra archive', s => { s.release.assets.push({ ...s.release.assets[0] }); }, 'asset_inventory'],
  ['duplicate name', s => { s.release.assets[1] = { ...s.release.assets[0], id: 400 }; }, 'duplicate_asset'],
  ['duplicate ID', s => { s.release.assets[1].id = s.release.assets[0].id; }, 'duplicate_asset'],
  ['unuploaded asset', s => { s.release.assets[0].state = 'new'; }, 'asset_metadata'],
  ['external asset URL', s => { s.release.assets[0].browser_download_url = 'https://example.com/file'; }, 'asset_metadata'],
  ['oversized archive', s => { s.release.assets[0].size = 513 * 1024 * 1024; }, 'asset_metadata'],
  ['wrong manifest pin', s => { s.inputs.checksumsDigest = `sha256:${'0'.repeat(64)}`; }, 'checksums_identity'],
  ['truncated manifest', s => { s.bodies.set('checksums.txt', s.bodies.get('checksums.txt').subarray(0, 10)); }, 'checksums_bytes'],
]) {
  test(`fails closed for ${name}`, async () => {
    const state = fixture(); change(state);
    const result = await state.run();
    assert.equal(result.status, 'failed');
    assert.equal(result.error, error);
  });
}

for (const kind of ['missing', 'duplicate', 'unexpected', 'path', 'wrong-digest']) {
  test(`rejects ${kind} checksum entries even when the manifest itself matches its pin`, async () => {
    const state = fixture();
    const lines = state.bodies.get('checksums.txt').toString().trim().split('\n');
    if (kind === 'missing') lines.pop();
    if (kind === 'duplicate') lines[1] = lines[0];
    if (kind === 'unexpected') lines[0] = `${'f'.repeat(64)}  unexpected.tar.gz`;
    if (kind === 'path') lines[0] = `${'f'.repeat(64)}  ../escape.tar.gz`;
    if (kind === 'wrong-digest') lines[0] = lines[0].replace(/^[0-9a-f]{64}/, 'f'.repeat(64));
    const body = Buffer.from(lines.join('\n') + '\n');
    state.bodies.set('checksums.txt', body);
    state.inputs.checksumsDigest = sha256(body);
    Object.assign(state.release.assets.find(a => a.name === 'checksums.txt'), { size: body.length, digest: sha256(body) });
    const result = await state.run();
    assert.equal(result.status, 'failed');
    assert.match(result.error, /checksum/);
    assert(!state.calls.some(url => url.endsWith('.tar.gz')));
  });
}

for (const kind of ['truncated', 'corrupted', 'oversized']) {
  test(`rejects a ${kind} archive and does not reach final identity verification`, async () => {
    const state = fixture();
    const name = archiveNames(state.inputs.tag)[0];
    const original = state.bodies.get(name);
    state.bodies.set(name, kind === 'truncated' ? original.subarray(0, 5) :
      kind === 'oversized' ? Buffer.concat([original, Buffer.from('extra')]) : Buffer.alloc(original.length, 1));
    const result = await state.run();
    assert.equal(result.status, 'failed');
    assert.match(result.error, /archive_bytes|response_too_large/);
    assert.equal(state.releaseReads, 1);
    assert(!result.downloads.some(asset => asset.name === name));
  });
}

test('retains partial byte counts after interrupted streaming without exposing network errors', async () => {
  const state = fixture();
  const name = archiveNames(state.inputs.tag)[0];
  state.respond = asset => {
    if (asset !== name) return;
    let sent = false;
    return new Response(new ReadableStream({ pull(controller) {
      if (!sent) { sent = true; controller.enqueue(Buffer.from('part')); }
      else controller.error(new Error('sensitive-signed-CDN-query'));
    } }));
  };
  const result = await state.run();
  assert.equal(result.status, 'failed');
  assert.equal(result.error, 'transport_failure');
  assert.equal(result.requests.find(r => r.url.endsWith(name)).bytes, 4);
  assert(!JSON.stringify(result).includes('sensitive-signed-CDN-query'));
});

test('follows anonymous allowlisted CDN redirects without retaining signed query strings', async () => {
  const state = fixture();
  state.respond = (name, target) => target.hostname === 'github.com' ?
    new Response(null, { status: 302, headers: { Location: `https://release-assets.githubusercontent.com/${name}?token=private` } }) : null;
  const result = await state.run();
  assert.equal(result.status, 'passed');
  assert(result.requests.filter(r => r.url.includes('/download/')).every(r => r.redirects === 1));
  assert(!JSON.stringify(result).includes('token=private'));
});

for (const location of ['http://release-assets.githubusercontent.com/file', 'https://evil.example/file',
  'https://user:password@github.com/file', 'https://github.com:444/file']) {
  test(`rejects unsafe redirect ${location}`, async () => {
    const state = fixture();
    state.respond = () => new Response(null, { status: 302, headers: { Location: location } });
    const result = await state.run();
    assert.equal(result.status, 'failed');
    assert.equal(result.error, 'unsafe_redirect');
    assert(!state.calls.includes(location));
  });
}

test('does not retry HTTP failures or redirect loops', async () => {
  for (const loop of [false, true]) {
    const state = fixture();
    state.respond = () => loop ? new Response(null, { status: 302,
      headers: { Location: 'https://github.com/loop' } }) : new Response('private error body', { status: 403 });
    const result = await state.run();
    assert.equal(result.error, loop ? 'redirect_limit' : 'http_status');
    assert.equal(state.calls.length, loop ? 9 : 4);
    assert(!JSON.stringify(result).includes('private error body'));
  }
});

test('rejects tag, asset and publication drift after downloads', async () => {
  for (const change of ['tag', 'asset', 'publication']) {
    const state = fixture();
    if (change === 'tag') state.finalTag = { ...state.tag, object: { type: 'commit', sha: 'd'.repeat(40) } };
    else {
      state.finalRelease = structuredClone(state.release);
      if (change === 'asset') state.finalRelease.assets[0].id = 1000;
      else state.finalRelease.published_at = '2026-02-01T00:00:00Z';
    }
    const result = await state.run();
    assert.equal(result.status, 'failed');
    assert.equal(result.phase, 'final_identity');
  }
});

test('rejects missing, malformed and shell-like inputs before any HTTP request', async () => {
  const state = fixture();
  validateInputs(state.inputs);
  for (const key of Object.keys(state.inputs)) {
    for (const value of ['', undefined, [], `${state.inputs[key]}\n`, `${state.inputs[key]};echo injected`]) {
      const result = await audit({ ...state.inputs, [key]: value }, { fetch: state.fetch });
      assert.equal(result.status, 'failed');
      assert.equal(result.phase, 'inputs');
    }
  }
  assert.equal(state.calls.length, 0);
});

test('workflow is manual, main-only, read-only, credential-free and retains failure evidence', () => {
  const workflow = fs.readFileSync(path.join(__dirname, '../workflows/release-download-audit.yml'), 'utf8');
  assert.match(workflow, /workflow_dispatch:/);
  assert.match(workflow, /github.ref == 'refs\/heads\/main'/);
  assert.match(workflow, /contents: read/);
  assert.match(workflow, /persist-credentials: false/);
  assert.match(workflow, /if: always\(\)/);
  assert.match(workflow, /if-no-files-found: error/);
  assert.doesNotMatch(workflow, /contents: write|packages: write|id-token: write|secrets\.|GH_TOKEN|GITHUB_TOKEN|pull_request_target/);
});

'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { validateAuditInputs, verifyDraftRelease } = require('./release-draft.cjs');

const commit = 'a'.repeat(40);
const tagObject = 'b'.repeat(40);
const inputs = {
  tag: 'v1.6.0', commit, tagObject,
  appDigest: `sha256:${'c'.repeat(64)}`,
  databaseDigest: `sha256:${'d'.repeat(64)}`,
  chartDigest: `sha256:${'e'.repeat(64)}`,
};

function fixture() {
  const release = { id: 42, tag_name: inputs.tag, draft: true, published_at: null };
  const state = {
    reference: { object: { type: 'tag', sha: tagObject } },
    tag: { tag: inputs.tag, object: { type: 'commit', sha: commit }, verification: { verified: true, reason: 'valid' } },
    releases: [release], release: { ...release }, calls: [],
  };
  const github = {
    rest: {
      git: {
        getRef: async args => { state.calls.push(['getRef', args]); return { data: state.reference }; },
        getTag: async args => { state.calls.push(['getTag', args]); return { data: state.tag }; },
      },
      repos: {
        listReleases: Symbol('listReleases'),
        getRelease: async args => { state.calls.push(['getRelease', args]); return { data: state.release }; },
      },
    },
    paginate: async (method, args) => {
      assert.equal(method, github.rest.repos.listReleases);
      assert.equal(args.per_page, 100);
      state.calls.push(['paginate', args]);
      return state.releases;
    },
  };
  return { state, github, args: { github, owner: 'example', repo: 'project', ...inputs } };
}

test('finds a signed draft by paginated list and numeric ID, never by the published-only tag endpoint', async () => {
  const { state, args } = fixture();
  assert.deepEqual(await verifyDraftRelease(args), {
    release_id: 42, tag: inputs.tag, commit, tag_object: tagObject, draft: true,
  });
  assert.deepEqual(state.calls.map(([method]) => method), ['getRef', 'getTag', 'paginate', 'getRelease']);
  assert.equal(state.calls[3][1].release_id, 42);
});

for (const [name, change, error] of [
  ['missing release', s => { s.releases = []; }, /exactly one/],
  ['ambiguous release', s => { s.releases.push({ ...s.release, id: 43 }); }, /exactly one/],
  ['published release', s => { s.releases[0].draft = false; }, /not a draft/],
  ['publication timestamp', s => { s.releases[0].published_at = '2026-01-01'; }, /not a draft/],
  ['malformed ID', s => { s.releases[0].id = '../42'; }, /Invalid release ID/],
  ['promotion after list', s => { s.release.draft = false; }, /changed during/],
  ['ID changed after list', s => { s.release.id = 43; }, /changed during/],
  ['tag changed after list', s => { s.release.tag_name = 'v1.5.2'; }, /changed during/],
  ['lightweight tag', s => { s.reference.object.type = 'commit'; }, /annotated signed/],
  ['moved tag object', s => { s.reference.object.sha = 'f'.repeat(40); }, /Tag object changed/],
  ['wrong commit', s => { s.tag.object.sha = 'f'.repeat(40); }, /expected commit/],
  ['wrong signed tag name', s => { s.tag.tag = 'v1.5.2'; }, /expected commit/],
  ['nested tag', s => { s.tag.object.type = 'tag'; }, /expected commit/],
  ['unsigned tag', s => { s.tag.verification.verified = false; }, /signature/],
  ['invalid signature reason', s => { s.tag.verification.reason = 'expired_key'; }, /signature/],
]) {
  test(`fails closed for ${name}`, async () => {
    const { state, args } = fixture();
    change(state);
    await assert.rejects(verifyDraftRelease(args), error);
  });
}

test('does not turn API permission or network errors into success', async () => {
  const { github, args } = fixture();
  github.paginate = async () => { throw new Error('HTTP 403'); };
  await assert.rejects(verifyDraftRelease(args), /HTTP 403/);
});

test('publishing gate can discover the signed tag object without a caller-supplied pin', async () => {
  const { args } = fixture();
  delete args.tagObject;
  assert.equal((await verifyDraftRelease(args)).tag_object, tagObject);
});

test('audit requires all immutable identity and digest inputs', () => {
  validateAuditInputs(inputs);
  for (const key of Object.keys(inputs)) {
    assert.throws(() => validateAuditInputs({ ...inputs, [key]: '' }));
    assert.throws(() => validateAuditInputs({ ...inputs, [key]: `${inputs[key]}\ninvalid` }));
    assert.throws(() => validateAuditInputs({ ...inputs, [key]: `${inputs[key]}\n` }));
    assert.throws(() => validateAuditInputs({ ...inputs, [key]: [inputs[key]] }));
  }
});

test('invalid identity is rejected before any network operation', async () => {
  const { state, args } = fixture();
  await assert.rejects(verifyDraftRelease({ ...args, tag: 'v1.6.0; echo injected' }), /Invalid release tag/);
  assert.equal(state.calls.length, 0);
});

'use strict';

// Shared by publishing and audit-only workflows. All API operations are reads.
// Drafts are not returned by getReleaseByTag; listing them needs push access.
function requireValue(condition, message) {
  if (!condition) throw new Error(message);
}

function validateIdentity({ tag, commit, tagObject }) {
  requireValue(typeof tag === 'string' && !/\s/.test(tag) &&
    /^v\d+\.\d+\.\d+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$/.test(tag), 'Invalid release tag');
  requireValue(typeof commit === 'string' && commit.length === 40 &&
    /^[0-9a-f]{40}$/.test(commit), 'Invalid release commit');
  if (tagObject !== undefined) {
    requireValue(typeof tagObject === 'string' && tagObject.length === 40 &&
      /^[0-9a-f]{40}$/.test(tagObject), 'Invalid tag object');
  }
}

function validateAuditInputs(inputs) {
  validateIdentity(inputs);
  requireValue(Boolean(inputs.tagObject), 'Audit requires an immutable tag object');
  for (const key of ['appDigest', 'databaseDigest', 'chartDigest']) {
    requireValue(typeof inputs[key] === 'string' && inputs[key].length === 71 &&
      /^sha256:[0-9a-f]{64}$/.test(inputs[key]), `Invalid ${key}`);
  }
}

async function verifyDraftRelease({ github, owner, repo, tag, commit, tagObject }) {
  validateIdentity({ tag, commit, tagObject });
  const reference = await github.rest.git.getRef({ owner, repo, ref: `tags/${tag}` });
  requireValue(reference.data.object.type === 'tag', 'Release requires an annotated signed tag');
  const object = reference.data.object.sha;
  if (tagObject !== undefined) requireValue(object === tagObject, 'Tag object changed');
  const signedTag = (await github.rest.git.getTag({ owner, repo, tag_sha: object })).data;
  requireValue(signedTag.tag === tag && signedTag.object.type === 'commit' &&
    signedTag.object.sha === commit, 'Tag does not identify the expected commit');
  requireValue(signedTag.verification?.verified === true &&
    signedTag.verification.reason === 'valid', 'Tag signature is not verified');

  const releases = await github.paginate(github.rest.repos.listReleases, { owner, repo, per_page: 100 });
  const matches = releases.filter(release => release.tag_name === tag);
  requireValue(matches.length === 1, 'Expected exactly one release for the tag');
  const candidate = matches[0];
  requireValue(Number.isSafeInteger(candidate.id) && candidate.id > 0, 'Invalid release ID');
  requireValue(candidate.draft === true && candidate.published_at === null, 'Release is not a draft');
  // Re-read the exact ID: do not accept a stale list entry after promotion.
  const release = (await github.rest.repos.getRelease({ owner, repo, release_id: candidate.id })).data;
  requireValue(release.id === candidate.id && release.tag_name === tag &&
    release.draft === true && release.published_at === null, 'Release changed during draft verification');
  return { release_id: release.id, tag, commit, tag_object: object, draft: true };
}

module.exports = { validateAuditInputs, verifyDraftRelease };

'use strict';

// Anonymous, read-only verification of an existing public release. Never builds,
// extracts or executes archives, accesses a cluster, or uses a GitHub token.
const fs = require('node:fs');
const crypto = require('node:crypto');
const API = 'https://api.github.com/repos/lithastra/kubeatlas';
const WEB = 'https://github.com/lithastra/kubeatlas/releases';
const CDN = new Set(['github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com']);
const sha256 = bytes => `sha256:${crypto.createHash('sha256').update(bytes).digest('hex')}`;
class AuditError extends Error {}
function requireValue(ok, code) { if (!ok) throw new AuditError(code); }

function validateInputs(inputs) {
  requireValue(typeof inputs.tag === 'string' && /^v\d+\.\d+\.\d+$/.test(inputs.tag) &&
    !/\s/.test(inputs.tag), 'invalid_tag');
  for (const key of ['commit', 'tagObject']) {
    requireValue(typeof inputs[key] === 'string' && /^[0-9a-f]{40}$/.test(inputs[key]) &&
      inputs[key].length === 40, `invalid_${key}`);
  }
  requireValue(typeof inputs.releaseID === 'string' && /^[1-9][0-9]*$/.test(inputs.releaseID) &&
    Number.isSafeInteger(Number(inputs.releaseID)) && String(Number(inputs.releaseID)) === inputs.releaseID,
  'invalid_release_id');
  requireValue(typeof inputs.checksumsDigest === 'string' && /^sha256:[0-9a-f]{64}$/.test(inputs.checksumsDigest) &&
    inputs.checksumsDigest.length === 71, 'invalid_checksums_digest');
}

function archiveNames(tag) {
  return ['kubeatlas', 'kubectl-atlas'].flatMap(binary =>
    (binary === 'kubeatlas' ? ['darwin', 'linux'] : ['darwin', 'linux', 'windows']).flatMap(os =>
      ['amd64', 'arm64'].map(arch => `${binary}_${tag.slice(1)}_${os}_${arch}.tar.gz`))).sort();
}

function inventory(release, inputs) {
  requireValue(release.id === Number(inputs.releaseID) && release.tag_name === inputs.tag &&
    release.draft === false && release.prerelease === false &&
    typeof release.published_at === 'string' && Number.isFinite(Date.parse(release.published_at)) &&
    release.html_url === `${WEB}/tag/${inputs.tag}`, 'release_identity');
  const names = ['checksums.txt', ...archiveNames(inputs.tag)].sort();
  requireValue(Array.isArray(release.assets) && release.assets.length === names.length, 'asset_inventory');
  const records = release.assets.map(asset => {
    requireValue(names.includes(asset.name) && asset.state === 'uploaded' &&
      Number.isSafeInteger(asset.id) && asset.id > 0 && Number.isSafeInteger(asset.size) &&
      asset.size > 0 && asset.size <= 512 * 1024 * 1024 &&
      typeof asset.digest === 'string' && /^sha256:[0-9a-f]{64}$/.test(asset.digest) && asset.digest.length === 71 &&
      asset.browser_download_url === `${WEB}/download/${inputs.tag}/${asset.name}` &&
      typeof asset.updated_at === 'string' && Number.isFinite(Date.parse(asset.updated_at)), 'asset_metadata');
    return { id: asset.id, name: asset.name, size: asset.size, digest: asset.digest,
      updatedAt: asset.updated_at, url: asset.browser_download_url };
  }).sort((a, b) => a.name.localeCompare(b.name));
  requireValue(new Set(records.map(a => a.name)).size === names.length &&
    new Set(records.map(a => a.id)).size === names.length, 'duplicate_asset');
  const checksums = records.find(asset => asset.name === 'checksums.txt');
  requireValue(checksums.digest === inputs.checksumsDigest && checksums.size <= 65536, 'checksums_identity');
  return records;
}

function parseChecksums(text, records) {
  const entries = new Map();
  for (const line of text.trim().split(/\r?\n/)) {
    const match = /^([0-9a-f]{64})  ([A-Za-z0-9_.-]+)$/.exec(line);
    requireValue(match && !entries.has(match[2]), 'invalid_checksum_entry');
    entries.set(match[2], `sha256:${match[1]}`);
  }
  const archives = records.filter(asset => asset.name !== 'checksums.txt');
  requireValue(entries.size === archives.length && archives.every(asset => entries.get(asset.name) === asset.digest),
    'checksum_inventory_or_digest');
  return entries;
}

async function audit(inputs, dependencies = {}) {
  const http = dependencies.fetch || fetch;
  const now = dependencies.now || (() => performance.now());
  const evidence = { status: 'failed', phase: 'inputs', inputs,
    auditToolsCommit: dependencies.auditToolsCommit || null,
    scope: 'Anonymous complete archive bytes, checksums and signed release identity; not native execution, OCI trust or endurance.',
    startedAt: new Date().toISOString(), requests: [], downloads: [] };

  async function request(url, { collect = false, maxBytes = 1024 * 1024, download = false } = {}) {
    const initial = new URL(url);
    let target = initial;
    const record = { url: initial.toString(), status: 'failed', bytes: 0, redirects: 0 };
    evidence.requests.push(record);
    const started = now();
    // No retries. A timeout covers redirects and the complete response body.
    const signal = AbortSignal.timeout(180000);
    try {
      for (let hop = 0; hop < 6; hop++) {
        requireValue(target.protocol === 'https:' && !target.username && !target.password && !target.port &&
          (download ? CDN.has(target.hostname) : target.hostname === 'api.github.com'), 'unsafe_redirect');
        const response = await http(target.toString(), { method: 'GET', redirect: 'manual', signal,
          credentials: 'omit', headers: { Accept: download ? 'application/octet-stream' : 'application/json',
            'Accept-Encoding': 'identity', 'User-Agent': 'kubeatlas-public-download-audit' } });
        record.httpStatus = response.status;
        record.finalHost = target.hostname;
        if ([301, 302, 303, 307, 308].includes(response.status)) {
          const location = response.headers.get('location');
          await response.body?.cancel();
          requireValue(download && location, 'unexpected_redirect');
          target = new URL(location, target);
          record.redirects++;
          continue;
        }
        if (response.status !== 200) {
          await response.body?.cancel();
          throw new AuditError('http_status');
        }
        requireValue(response.body, 'missing_body');
        const chunks = [];
        const hash = crypto.createHash('sha256');
        for await (const chunk of response.body) {
          record.bytes += chunk.length;
          requireValue(record.bytes <= maxBytes, 'response_too_large');
          hash.update(chunk);
          if (collect) chunks.push(chunk);
        }
        record.digest = `sha256:${hash.digest('hex')}`;
        record.status = 'complete';
        return { ...record, text: collect ? Buffer.concat(chunks).toString('utf8') : undefined };
      }
      throw new AuditError('redirect_limit');
    } catch (error) {
      // Do not retain raw network errors or signed CDN query strings.
      record.error = error instanceof AuditError ? error.message : signal.aborted ? 'timeout' : 'transport_failure';
      throw new AuditError(record.error);
    } finally { record.durationMs = now() - started; }
  }
  const json = async route => JSON.parse((await request(`${API}/${route}`, { collect: true })).text);
  async function identity() {
    const ref = await json(`git/ref/tags/${inputs.tag}`);
    requireValue(ref.object?.type === 'tag' && ref.object.sha === inputs.tagObject, 'tag_object_drift');
    const tag = await json(`git/tags/${inputs.tagObject}`);
    requireValue(tag.sha === inputs.tagObject && tag.tag === inputs.tag && tag.object?.type === 'commit' &&
      tag.object.sha === inputs.commit && tag.verification?.verified === true &&
      tag.verification.reason === 'valid', 'tag_signature_or_commit');
    const release = await json(`releases/${inputs.releaseID}`);
    return { publishedAt: release.published_at, assets: inventory(release, inputs) };
  }
  try {
    validateInputs(inputs);
    evidence.phase = 'initial_identity';
    const before = await identity();
    evidence.release = before;
    evidence.phase = 'checksums';
    const checksum = before.assets.find(a => a.name === 'checksums.txt');
    const manifest = await request(checksum.url, { download: true, collect: true, maxBytes: checksum.size });
    requireValue(manifest.bytes === checksum.size && manifest.digest === inputs.checksumsDigest, 'checksums_bytes');
    parseChecksums(manifest.text, before.assets);
    evidence.downloads.push({ name: checksum.name, id: checksum.id, size: manifest.bytes, digest: manifest.digest });
    evidence.phase = 'archives';
    const archives = before.assets.filter(a => a.name !== 'checksums.txt');
    let index = 0, failed = false;
    async function worker() {
      while (!failed && index < archives.length) {
        const asset = archives[index++];
        try {
          const body = await request(asset.url, { download: true, maxBytes: asset.size });
          requireValue(body.bytes === asset.size && body.digest === asset.digest, 'archive_bytes');
          evidence.downloads.push({ name: asset.name, id: asset.id, size: body.bytes, digest: body.digest });
        } catch (error) { failed = true; throw error; }
      }
    }
    const results = await Promise.allSettled([worker(), worker(), worker()]);
    const failure = results.find(result => result.status === 'rejected');
    if (failure) throw failure.reason;
    requireValue(evidence.downloads.length === 11, 'incomplete_downloads');
    evidence.phase = 'final_identity';
    requireValue(JSON.stringify(await identity()) === JSON.stringify(before), 'release_changed_during_download');
    evidence.downloads.sort((a, b) => a.name.localeCompare(b.name));
    evidence.totalBytes = evidence.downloads.reduce((sum, asset) => sum + asset.size, 0);
    evidence.phase = 'complete';
    evidence.status = 'passed';
  } catch (error) {
    evidence.error = error instanceof AuditError ? error.message : 'unexpected_failure';
  }
  evidence.finishedAt = new Date().toISOString();
  return evidence;
}

if (require.main === module) {
  const inputs = { tag: process.env.RELEASE_TAG, commit: process.env.RELEASE_COMMIT,
    tagObject: process.env.RELEASE_TAG_OBJECT, releaseID: process.env.RELEASE_ID,
    checksumsDigest: process.env.CHECKSUMS_DIGEST };
  audit(inputs, { auditToolsCommit: process.env.GITHUB_SHA }).then(evidence => {
    const output = `${JSON.stringify(evidence, null, 2)}\n`;
    if (process.env.DOWNLOAD_EVIDENCE_FILE) fs.writeFileSync(process.env.DOWNLOAD_EVIDENCE_FILE, output);
    else process.stdout.write(output);
    if (evidence.status !== 'passed') process.exitCode = 1;
  }).catch(() => { console.error('Unable to retain public download evidence'); process.exitCode = 1; });
}

module.exports = { validateInputs, archiveNames, inventory, parseChecksums, audit, sha256 };

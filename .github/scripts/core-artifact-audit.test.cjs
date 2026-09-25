'use strict';

// Exercise the real audit shell wiring with isolated command doubles. These
// tests make no network requests and never invoke Docker or a Kubernetes API.
// Signature/attestation internals are outside this fixture's scope.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync, execFileSync } = require('node:child_process');
const root = path.resolve(__dirname, '../..');
const digest = `sha256:${'a'.repeat(64)}`;

const commandDouble = `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const command = path.basename(process.argv[1]);
const args = process.argv.slice(2);
const env = process.env;
fs.appendFileSync(env.MOCK_LOG, JSON.stringify({ command, args }) + '\\n');
switch (command) {
  case 'oras':
    assert.equal(args[0], 'resolve');
    console.log(env.MOCK_RESOLVED_DIGEST || env.MOCK_DIGEST);
    break;
  case 'helm':
    if (args[0] === 'pull') {
      assert.ok(args[1].endsWith('@' + env.MOCK_DIGEST));
      const directory = args[args.indexOf('--destination') + 1];
      for (const name of JSON.parse(env.MOCK_ARCHIVES)) {
        fs.copyFileSync(env.MOCK_ARCHIVE, path.join(directory, name));
      }
    } else if (args[0] === 'install') {
      assert.ok(fs.statSync(args[2]).isFile());
    } else {
      assert.ok(['lint', 'uninstall'].includes(args[0]));
    }
    break;
  case 'kubectl':
    assert.ok(['get', 'rollout', 'delete'].includes(args[0]));
    if (args[0] === 'get') process.stdout.write('ghcr.io/lithastra/kubeatlas@' + env.MOCK_DIGEST);
    break;
  case 'cosign': assert.equal(args[0], 'verify'); break;
  case 'docker': assert.equal(args[0], 'pull'); break;
  case 'bash': assert.ok(args[0].endsWith('/test/verify/image-attestations.sh')); break;
  default: throw new Error('Unexpected command: ' + command);
}
`;

function audit(t, archives, { version = '1.6.0', resolvedDigest = digest } = {}) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'kubeatlas-audit-test-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const bin = path.join(directory, 'bin');
  const chart = path.join(directory, 'kubeatlas');
  fs.mkdirSync(bin);
  fs.mkdirSync(chart);
  const database = fs.readFileSync(path.join(root, 'images/postgres-age/image.env'), 'utf8');
  const value = name => database.match(new RegExp('^' + name + '=(.+)$', 'm'))[1];
  fs.writeFileSync(path.join(chart, 'Chart.yaml'), `name: kubeatlas\nversion: ${version}\nappVersion: "${version}"\n`);
  fs.writeFileSync(path.join(chart, 'values.yaml'),
    `persistence:\n  embedded:\n    image: ${value('POSTGRES_AGE_REPOSITORY')}:${value('POSTGRES_AGE_TAG')}\n`);
  const archive = path.join(directory, 'fixture.tgz');
  execFileSync('tar', ['-czf', archive, '-C', directory, 'kubeatlas']);
  for (const command of ['oras', 'cosign', 'helm', 'kubectl', 'docker', 'bash']) {
    fs.writeFileSync(path.join(bin, command), commandDouble, { mode: 0o755 });
  }
  fs.writeFileSync(path.join(directory, 'config.json'), '{"auths":{}}');
  const evidenceFile = path.join(directory, 'evidence.json');
  const log = path.join(directory, 'calls.jsonl');
  const result = spawnSync('/bin/bash', [path.join(root, 'test/verify/core-artifact-audit.sh')], {
    encoding: 'utf8', timeout: 15000,
    env: {
      ...process.env, PATH: `${bin}:${process.env.PATH}`,
      MOCK_LOG: log, MOCK_ARCHIVES: JSON.stringify(archives), MOCK_ARCHIVE: archive,
      MOCK_DIGEST: digest, MOCK_RESOLVED_DIGEST: resolvedDigest,
      KUBEATLAS_RELEASE_TAG: 'v1.6.0', KUBEATLAS_RELEASE_COMMIT: 'b'.repeat(40),
      KUBEATLAS_RELEASE_SOURCE_DIR: root, KUBEATLAS_ARTIFACT_EVIDENCE_FILE: evidenceFile,
      KUBEATLAS_EXPECTED_APP_DIGEST: digest, KUBEATLAS_EXPECTED_DATABASE_DIGEST: digest,
      KUBEATLAS_EXPECTED_CHART_DIGEST: digest, KUBEATLAS_REQUIRE_ANONYMOUS: 'true',
      DOCKER_CONFIG: directory, HELM_REGISTRY_CONFIG: path.join(directory, 'config.json'),
    },
  });
  assert.ifError(result.error);
  const calls = fs.readFileSync(log, 'utf8').trim().split('\n').map(line => JSON.parse(line));
  const evidence = JSON.parse(fs.readFileSync(evidenceFile, 'utf8'));
  return { result, calls, evidence };
}

// Helm 3.20 replaces the last colon in the OCI reference when naming a pull.
for (const name of ['kubeatlas-1.6.0.tgz', `kubeatlas@sha256-${digest.slice(7)}.tgz`]) {
  test(`installs the verified archive regardless of Helm filename: ${name}`, t => {
    const { result, calls, evidence } = audit(t, [name]);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(evidence.status, 'passed');
    const install = calls.find(call => call.command === 'helm' && call.args[0] === 'install');
    assert.equal(path.basename(install.args[2]), name);
  });
}

for (const [name, archives, options, message] of [
  ['missing archive', [], {}, /exactly one downloaded/],
  ['ambiguous archives', ['one.tgz', 'two.tgz'], {}, /exactly one downloaded/],
  ['wrong chart version', ['digest.tgz'], { version: '1.5.2' }, /chart version does not match/],
  ['digest drift', ['digest.tgz'], { resolvedDigest: `sha256:${'c'.repeat(64)}` }, /no longer resolves/],
]) {
  test(`fails closed before installation for ${name}`, t => {
    const { result, calls, evidence } = audit(t, archives, options);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, message);
    assert.equal(evidence.status, 'failed');
    assert.equal(calls.some(call => call.command === 'helm' && call.args[0] === 'install'), false);
  });
}

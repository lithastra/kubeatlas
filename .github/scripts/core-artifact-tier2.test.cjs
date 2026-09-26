'use strict';

// Run the actual orchestration against command/HTTP doubles. These tests never
// contact a registry, Docker, or Kubernetes and are not live release evidence.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { audit, CONTEXT, NS, OPERATOR_NS, sha256, runCommand } = require('./core-artifact-tier2.cjs');

async function fixture(t, options = {}) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'tier2-audit-fixture-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  fs.writeFileSync(path.join(directory, 'config.json'), JSON.stringify(options.credentials ? { credsStore: 'desktop' } : { auths: {} }));
  fs.mkdirSync(path.join(directory, 'images/postgres-age'), { recursive: true });
  fs.writeFileSync(path.join(directory, 'images/postgres-age/image.env'),
    'POSTGRES_AGE_REPOSITORY=ghcr.io/lithastra/postgres-age\nPOSTGRES_AGE_TAG=16.15-age1.6.0-rc0.2\n');
  const archive = Buffer.from('verified mock chart archive');
  const blobs = new Map();
  const register = value => { const body = JSON.stringify(value); const digest = sha256(body); blobs.set(digest, body); return digest; };
  const images = {};
  for (const [name, letter] of [['application', 'a'], ['database', 'b']]) {
    const configDigest = `sha256:${letter.repeat(64)}`;
    const platformDigest = register({ config: { digest: configDigest } });
    const digest = register({ manifests: [{ platform: { os: 'linux', architecture: 'amd64' }, digest: platformDigest }] });
    images[name] = { digest, platformDigest, configDigest };
  }
  const chartDigest = register({ layers: [{ mediaType: 'application/vnd.cncf.helm.chart.content.v1.tar+gzip', digest: sha256(archive) }] });
  const env = { GITHUB_ACTIONS: 'true', GITHUB_SHA: 'f'.repeat(40), KUBEATLAS_PRIOR_AUDIT_RESULT: 'success',
    KUBEATLAS_DRAFT_RELEASE_ID: '42', KUBEATLAS_RELEASE_TAG: 'v1.6.0', KUBEATLAS_RELEASE_COMMIT: 'c'.repeat(40),
    KUBEATLAS_RELEASE_TAG_OBJECT: 'd'.repeat(40), KUBEATLAS_EXPECTED_APP_DIGEST: images.application.digest,
    KUBEATLAS_EXPECTED_DATABASE_DIGEST: images.database.digest, KUBEATLAS_EXPECTED_CHART_DIGEST: chartDigest,
    KUBEATLAS_RELEASE_SOURCE_DIR: directory, DOCKER_CONFIG: directory, HELM_REGISTRY_CONFIG: path.join(directory, 'config.json'), ...options.env };
  const calls = [], namespaces = new Map();
  let clock = 0, sequence = 0, sentinel = '', stopped = false;
  const json = JSON.stringify;
  async function run(command, rawArgs, execution = {}) {
    calls.push({ command, args: rawArgs, timeout: execution.timeout }); clock += 1;
    if (command === 'oras') {
      if (rawArgs[0] === 'resolve') return options.drift ? `sha256:${'0'.repeat(64)}` :
        rawArgs[1].includes('/charts/') ? chartDigest : rawArgs[1].includes('/postgres-age:') ? images.database.digest : images.application.digest;
      assert.deepEqual(rawArgs.slice(0, 3), ['manifest', 'fetch', '--output']);
      fs.writeFileSync(rawArgs[3], options.badManifest ? '{}' : blobs.get(rawArgs[4].split('@')[1])); return '';
    }
    if (command === 'helm') {
      assert.deepEqual(rawArgs.slice(0, 2), ['--kube-context', CONTEXT]); const args = rawArgs.slice(2);
      if (args[0] === 'pull') {
        assert.equal(args[1], `oci://ghcr.io/lithastra/charts/kubeatlas@${chartDigest}`);
        const target = args[args.indexOf('--destination') + 1];
        fs.writeFileSync(path.join(target, 'digest-named-archive.tgz'), options.badArchive ? 'corrupt' : archive);
        if (options.extraArchive) fs.writeFileSync(path.join(target, 'extra.tgz'), archive);
      } else if (args[0] === 'show') {
        return args[1] === 'chart' ? `name: kubeatlas\nversion: ${options.chartVersion || '1.6.0'}\nappVersion: "1.6.0"\n` :
          'persistence:\n  embedded:\n    image: ghcr.io/lithastra/postgres-age:16.15-age1.6.0-rc0.2\n';
      } else if (args[0] === 'install') {
        const operator = args[1] === 'cnpg';
        assert.equal(args[args.indexOf('--timeout') + 1], operator ? '5m' : '10m');
        assert.equal(execution.timeout, operator ? 330000 : 630000);
        if (operator && options.operatorFailure) throw new Error('simulated command failure, not retained');
        if (!operator) {
          assert.ok(args.includes(`image.digest=${images.application.digest}`));
          assert.ok(args.includes(`persistence.embedded.image=ghcr.io/lithastra/postgres-age:16.15-age1.6.0-rc0.2@${images.database.digest}`));
          if (options.installFailure) throw new Error('simulated installation timeout');
        }
      } else assert.equal(args[0], 'lint');
      return '';
    }
    assert.equal(command, 'kubectl');
    assert.deepEqual(rawArgs.slice(0, 3), ['--context', CONTEXT, '--request-timeout=30s']);
    const args = rawArgs.slice(3);
    assert.equal(args.includes('secret'), false, 'never read generated database credentials');
    if (args[0] === 'config') return options.context || CONTEXT;
    if (args[0] === 'wait') return '';
    if (args[0] === 'create') {
      if (args[1] === 'namespace') {
        if (options.existingNamespace) throw new Error('AlreadyExists');
        namespaces.set(args[2], args[2] + '-uid');
        return json({ metadata: { uid: namespaces.get(args[2]) } });
      }
      const resource = JSON.parse(execution.input); assert.equal(resource.kind, 'Secret');
      sentinel = resource.stringData.value; return 'secret/evidence-sentinel created';
    }
    if (args[0] === 'apply') {
      if (options.lateApply) clock += 31000;
      const resource = JSON.parse(execution.input); sequence = Number(resource.data.sequence);
      return json({ metadata: { uid: '12345678-1234-1234-1234-123456789012', resourceVersion: String(sequence) } });
    }
    if (args[0] === 'get') {
      if (args[1] === 'nodes') return json({ items: [{ metadata: { name: 'kubeatlas-core-tier2-audit-control-plane', uid: 'node-uid' },
        status: { nodeInfo: { architecture: 'amd64', kubeletVersion: 'v1.36.1' } } }] });
      if (args[1] === 'namespace') return json({ metadata: { uid: options.changedNamespace ? 'different-owner' : namespaces.get(args[2]) } });
      if (args[1] === 'deployment') return json({ spec: { template: { spec: { containers: [{ name: args[2] === 'cnpg-cloudnative-pg' ? 'manager' : 'kubeatlas',
        image: args[2] === 'cnpg-cloudnative-pg' ? 'ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0' : `ghcr.io/lithastra/kubeatlas@${options.deploymentDrift ? 'sha256:bad' : images.application.digest}` }] } } } });
      if (args[1] === 'cluster.postgresql.cnpg.io') return json({ spec: { imageName: `ghcr.io/lithastra/postgres-age:16.15-age1.6.0-rc0.2@${images.database.digest}` }, status: { readyInstances: 1 } });
      if (args[1] === 'pvc') return json({ items: [{ metadata: { uid: 'pvc-uid' }, status: { phase: 'Bound' } }] });
      if (args[1] === 'pods') return json({ items: Object.entries(images).map(([role, image]) => ({
        metadata: { name: role, uid: options.podReplacement && sequence > 1 ? role + '-new' : role + '-uid' },
        status: { phase: 'Running', conditions: [{ type: 'Ready', status: options.notReady ? 'False' : 'True' }],
          initContainerStatuses: [{ name: 'init', restartCount: options.initRestart ? 1 : 0 }],
          containerStatuses: [{ name: role === 'application' ? 'kubeatlas' : 'postgres', ready: true,
          restartCount: options.restart ? 1 : 0, lastState: options.oom ? { terminated: { reason: 'OOMKilled' } } : {},
          imageID: 'ghcr.io/example@' + (options.runtimeDrift ? 'sha256:bad' : image[options.imageKind || 'platformDigest']) }] } })) });
    }
    if (args[0] === '-n') {
      assert.equal(args[2], 'exec'); assert.ok(args.includes('psql'));
      const query = args.at(-1);
      if (query.includes('server_version')) return json({ postgres: '16.15 (Debian)', age: '1.6.0' });
      if (query.includes("'matching'")) { if (options.lateHistory) clock += 31000; return json({ matching: options.missingHistory ? 0 : 1 }); }
      assert.ok(query.includes('unsafe_secret_rows'));
      return json({ history_payloads: 0, unsafe_secret_rows: options.unsafeDatabase ? 1 : 0 });
    }
    if (args[0] === 'logs') return options.logLeak ? sentinel : 'safe application log';
    if (args[0] === 'delete') {
      assert.equal(args[1], 'namespace'); assert.ok([NS, OPERATOR_NS].includes(args[2]));
      if (options.cleanupFailure) throw new Error('cleanup failure');
      namespaces.delete(args[2]); return '';
    }
    throw new Error('Unexpected command double: ' + args.join(' '));
  }
  async function http(url) {
    assert.ok(url.startsWith('http://127.0.0.1:18086/'));
    const route = new URL(url).pathname; let body = {};
    if (options.endpointFailure && route === '/api/v1/diagnose') return new Response('{}', { status: 500 });
    if (route === '/api/v1/info') body = { version: '1.6.0', commit: options.wrongCommit ? 'bad' : env.KUBEATLAS_RELEASE_COMMIT, graphstore_version: 'v2' };
    if (route.endsWith('/ConfigMap/functional-canary')) {
      if (options.lateUpdate) clock += 31000;
      body = { resource: { resourceVersion: options.wrongResourceVersion ? '0' : String(sequence),
        annotations: { 'kubeatlas.io/functional-sequence': String(sequence) } } };
    }
    if (route === '/') return new Response('<html><script src="/assets/main.js"></script></html>');
    if (route === '/metrics') return new Response([
      'kubeatlas_informer_synced 1', 'kubeatlas_kubernetes_api_reachable 1', 'kubeatlas_storage_reachable 1',
      `kubeatlas_storage_durable ${options.healthFailure ? 0 : 1}`, 'kubeatlas_graph_observation_state{state="synced"} 1',
      `kubeatlas_snapshot_queue_drop_total ${options.queueDrop ? 1 : 0}`, `kubeatlas_snapshot_write_failed_total ${options.writeFailure ? 1 : 0}`,
      `kubeatlas_snapshot_queue_depth ${options.queueDepth ? 1 : 0}`, 'kubeatlas_go_memory_limit_bytes 1000000', 'kubeatlas_backup_status_available 0',
    ].join('\n'));
    if (route.includes('/Secret/') && options.apiLeak) body = { unexpected: options.apiLeak === 'base64' ? Buffer.from(sentinel).toString('base64') : sentinel };
    return new Response(json(body));
  }
  const evidence = await audit(env, { run, fetch: http, now: () => clock,
    sleep: async ms => { clock += ms; }, startForward: () => ({ alive: () => true, stop: async () => { stopped = true; } }) });
  assert.ok(!sentinel || !json(evidence).includes(sentinel), 'raw sentinel is not retained');
  assert.ok(!sentinel || !json(evidence).includes(Buffer.from(sentinel).toString('base64')), 'encoded sentinel is not retained');
  return { evidence, calls, stopped, namespaces };
}

for (const imageKind of ['digest', 'platformDigest', 'configDigest']) {
  test(`passes with an OCI-bound runtime ${imageKind}`, async t => {
    const { evidence, calls, stopped, namespaces } = await fixture(t, { imageKind });
    assert.equal(evidence.status, 'passed', JSON.stringify(evidence.failure));
    assert.equal(evidence.cleanupPassed, true); assert.equal(evidence.samples.length, 3);
    assert.equal(stopped, true); assert.equal(namespaces.size, 0);
    const deleted = calls.filter(c => c.command === 'kubectl' && c.args[3] === 'delete').map(c => c.args[5]);
    assert.deepEqual(deleted, [NS, OPERATOR_NS]);
  });
}

for (const [name, options, phase, code] of [
  ['missing input', { env: { KUBEATLAS_EXPECTED_APP_DIGEST: '' } }, 'preflight', 'invalid_identity_inputs'],
  ['failed prior audit', { env: { KUBEATLAS_PRIOR_AUDIT_RESULT: 'failure' } }, 'preflight', 'missing_ci_audit_gate'],
  ['not CI', { env: { GITHUB_ACTIONS: 'false' } }, 'preflight', 'missing_ci_audit_gate'],
  ['registry credentials', { credentials: true }, 'preflight', 'registry_credentials_present'],
  ['wrong cluster', { context: 'docker-desktop' }, 'preflight', 'wrong_cluster_context'],
  ['tag drift', { drift: true }, 'verify_artifacts', 'tag_digest_drift'],
  ['corrupt manifest', { badManifest: true }, 'verify_artifacts', 'manifest_digest'],
  ['corrupt chart', { badArchive: true }, 'verify_artifacts', 'chart_archive_digest'],
  ['ambiguous chart', { extraArchive: true }, 'verify_artifacts', 'chart_archive_inventory'],
  ['wrong chart version', { chartVersion: '1.5.2' }, 'verify_artifacts', 'chart_version'],
  ['existing namespace', { existingNamespace: true }, 'install_operator', 'unexpected_failure'],
  ['operator timeout', { operatorFailure: true }, 'install_operator', 'unexpected_failure'],
  ['install timeout', { installFailure: true }, 'install_tier2', 'unexpected_failure'],
  ['deployment drift', { deploymentDrift: true }, 'install_tier2', 'application_pin'],
  ['slow mutation request', { lateApply: true }, 'functional', 'update_deadline'],
  ['late successful update', { lateUpdate: true }, 'functional', 'update_deadline'],
  ['wrong exact resource version', { wrongResourceVersion: true }, 'functional', 'update_deadline'],
  ['late successful history', { lateHistory: true }, 'functional', 'history_deadline'],
  ['missing history', { missingHistory: true }, 'functional', 'history_deadline'],
  ['wrong runtime commit', { wrongCommit: true }, 'functional', 'runtime_identity'],
  ['endpoint error', { endpointFailure: true }, 'functional', 'http_status'],
  ['not durable', { healthFailure: true }, 'functional', 'health_metric'],
  ['queue drop', { queueDrop: true }, 'functional', 'normal_load_error'],
  ['write failure', { writeFailure: true }, 'functional', 'normal_load_error'],
  ['queue backlog', { queueDepth: true }, 'functional', 'normal_load_error'],
  ['unsafe Secret storage', { unsafeDatabase: true }, 'functional', 'database_safety'],
  ['pod restart', { restart: true }, 'functional', 'pod_health'],
  ['init container restart', { initRestart: true }, 'functional', 'pod_health'],
  ['pod not Ready', { notReady: true }, 'functional', 'pod_health'],
  ['OOM', { oom: true }, 'functional', 'pod_health'],
  ['runtime image drift', { runtimeDrift: true }, 'functional', 'runtime_image'],
  ['pod replacement', { podReplacement: true }, 'functional', 'pod_replacement'],
  ['raw API sentinel leak', { apiLeak: true }, 'functional', 'sentinel_leak'],
  ['encoded API sentinel leak', { apiLeak: 'base64' }, 'functional', 'sentinel_leak'],
  ['log sentinel leak', { logLeak: true }, 'functional', 'sentinel_leak'],
]) {
  test(`fails closed for ${name}`, async t => {
    const { evidence, calls } = await fixture(t, options);
    assert.equal(evidence.status, 'failed');
    assert.deepEqual(evidence.failure, { phase, code });
    if (['preflight', 'verify_artifacts'].includes(phase))
      assert.equal(calls.some(c => c.command === 'helm' && c.args[2] === 'install'), false);
    if (phase === 'install_operator')
      assert.equal(calls.some(c => c.command === 'helm' && c.args[3] === 'kubeatlas-tier2'), false);
  });
}

test('command errors do not retain stderr or request bodies', async () => {
  await assert.rejects(runCommand(process.execPath, ['-e', "process.stderr.write('sensitive-test-marker'); process.exit(1)"]),
    error => error.message.endsWith('_failed') && !error.message.includes('sensitive-test-marker'));
});

test('command deadlines fail closed without echoing output', async () => {
  await assert.rejects(runCommand(process.execPath, ['-e', 'setTimeout(() => {}, 10000)'], { timeout: 50 }),
    error => error.message.endsWith('_timeout'));
});

for (const options of [{ cleanupFailure: true }, { changedNamespace: true }]) {
  test(`cleanup cannot turn a failure into success: ${JSON.stringify(options)}`, async t => {
    const { evidence, calls } = await fixture(t, options);
    assert.equal(evidence.samples.length, 3); assert.equal(evidence.status, 'failed');
    assert.equal(evidence.cleanupPassed, false);
    if (options.changedNamespace) assert.equal(calls.some(c => c.args[3] === 'delete'), false);
  });
}

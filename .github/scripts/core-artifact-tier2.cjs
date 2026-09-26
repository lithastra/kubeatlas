'use strict';

// CI-only installation check of already signed public artifacts. No build,
// publication, existing-cluster access, raw Secret reads, or backup access.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const { execFile, spawn } = require('node:child_process');
const { promisify } = require('node:util');
const { validateAuditInputs } = require('./release-draft.cjs');
const execute = promisify(execFile);
const CONTEXT = 'kind-kubeatlas-core-tier2-audit';
const NS = 'kubeatlas-core-tier2-audit';
const OPERATOR_NS = 'kubeatlas-audit-cnpg';
const RELEASE = 'kubeatlas-tier2';
const APP = 'ghcr.io/lithastra/kubeatlas';
const DATABASE = 'ghcr.io/lithastra/postgres-age';
const CHART = 'ghcr.io/lithastra/charts/kubeatlas';
const sha256 = data => `sha256:${crypto.createHash('sha256').update(data).digest('hex')}`;
class GateError extends Error {}
function requireValue(ok, code) { if (!ok) throw new GateError(code); }

async function runCommand(command, args, options = {}) {
  try {
    const child = execute(command, args, { encoding: 'utf8', timeout: options.timeout || 60000,
      maxBuffer: 32 * 1024 * 1024 });
    child.child.stdin.end(options.input);
    return (await child).stdout;
  } catch (error) {
    // A CLI error can echo request bodies. Never retain stdout/stderr here.
    throw new GateError(`${command}_${error.killed ? 'timeout' : 'failed'}`);
  }
}

function startForward() {
  const child = spawn('kubectl', ['--context', CONTEXT, '-n', NS, 'port-forward',
    `service/${RELEASE}`, '--address', '127.0.0.1', '18086:80'], { stdio: 'ignore' });
  let failed = false;
  const done = new Promise(resolve => {
    child.on('error', () => { failed = true; resolve(); });
    child.on('close', resolve);
  });
  return { alive: () => !failed && child.exitCode === null,
    stop: async () => { if (child.exitCode === null) child.kill('SIGTERM'); await done; } };
}

function anonymousConfig(file) {
  const config = JSON.parse(fs.readFileSync(file, 'utf8'));
  requireValue(Object.keys(config).every(key => key === 'auths') &&
    Object.keys(config.auths || {}).length === 0, 'registry_credentials_present');
}

async function audit(env = process.env, dependencies = {}) {
  const run = dependencies.run || runCommand;
  const http = dependencies.fetch || fetch;
  const now = dependencies.now || (() => performance.now());
  const sleep = dependencies.sleep || (ms => new Promise(resolve => setTimeout(resolve, ms)));
  const forwardFactory = dependencies.startForward || startForward;
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'kubeatlas-public-tier2-'));
  const owned = [];
  const token = crypto.randomBytes(32).toString('hex');
  const needles = [token, Buffer.from(token).toString('base64')];
  const scan = value => requireValue(!needles.some(needle => value.includes(needle)), 'sentinel_leak');
  const inputs = { tag: env.KUBEATLAS_RELEASE_TAG, commit: env.KUBEATLAS_RELEASE_COMMIT,
    tagObject: env.KUBEATLAS_RELEASE_TAG_OBJECT, appDigest: env.KUBEATLAS_EXPECTED_APP_DIGEST,
    databaseDigest: env.KUBEATLAS_EXPECTED_DATABASE_DIGEST, chartDigest: env.KUBEATLAS_EXPECTED_CHART_DIGEST };
  const evidence = { status: 'failed', phase: 'preflight', release: inputs,
    auditToolsCommit: env.GITHUB_SHA, priorAuditResult: env.KUBEATLAS_PRIOR_AUDIT_RESULT,
    scope: 'Public Tier 2 installation and three functional samples; not endurance, recovery, HA, or binary-download validation.',
    startedAt: new Date().toISOString(), samples: [], cleanupPassed: false };
  let forward, completed = false;
  const kube = (args, options) => run('kubectl', ['--context', CONTEXT, '--request-timeout=30s', ...args], options);
  const helm = (args, options) => run('helm', ['--kube-context', CONTEXT, ...args], options);
  const json = async (args, options) => JSON.parse(await kube(args, options));
  const sql = async query => json(['-n', NS, 'exec', `${RELEASE}-pg-1`, '-c', 'postgres', '--',
    'psql', '-XAt', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'kubeatlas', '-c', query]);
  const phase = name => { evidence.phase = name; };
  async function request(route, allowed = [200]) {
    const started = now();
    let response;
    try { response = await http(`http://127.0.0.1:18086${route}`, { signal: AbortSignal.timeout(15000) }); }
    catch { throw new GateError('http_transport'); }
    const body = await response.text();
    requireValue(Buffer.byteLength(body) <= 32 * 1024 * 1024, 'response_size');
    scan(body);
    requireValue(allowed.includes(response.status), 'http_status');
    return { body, status: response.status, latencyMs: now() - started };
  }
  async function manifest(repository, digest, name) {
    const file = path.join(root, `${name}.json`);
    await run('oras', ['manifest', 'fetch', '--output', file, `${repository}@${digest}`]);
    const bytes = fs.readFileSync(file);
    requireValue(sha256(bytes) === digest, 'manifest_digest');
    return JSON.parse(bytes);
  }
  async function createNamespace(name) {
    const created = await json(['create', 'namespace', name, '-o', 'json']);
    requireValue(typeof created.metadata?.uid === 'string' && created.metadata.uid.length > 0, 'namespace_identity');
    owned.push({ name, uid: created.metadata.uid });
  }
  try {
    try { validateAuditInputs(inputs); } catch { throw new GateError('invalid_identity_inputs'); }
    requireValue(env.GITHUB_ACTIONS === 'true' && env.KUBEATLAS_PRIOR_AUDIT_RESULT === 'success', 'missing_ci_audit_gate');
    requireValue(/^[0-9a-f]{40}$/.test(env.GITHUB_SHA || ''), 'invalid_tools_commit');
    requireValue(/^[1-9][0-9]*$/.test(env.KUBEATLAS_DRAFT_RELEASE_ID || ''), 'invalid_draft_id');
    evidence.draftReleaseID = env.KUBEATLAS_DRAFT_RELEASE_ID;
    anonymousConfig(path.join(env.DOCKER_CONFIG, 'config.json'));
    anonymousConfig(env.HELM_REGISTRY_CONFIG);
    requireValue((await kube(['config', 'current-context'])).trim() === CONTEXT, 'wrong_cluster_context');
    const nodes = await json(['get', 'nodes', '-o', 'json']);
    requireValue(nodes.items.length === 1 && nodes.items[0].metadata.name === 'kubeatlas-core-tier2-audit-control-plane' &&
      nodes.items[0].status.nodeInfo.architecture === 'amd64' &&
      nodes.items[0].status.nodeInfo.kubeletVersion === 'v1.36.1', 'wrong_cluster_identity');
    evidence.node = { uid: nodes.items[0].metadata.uid, architecture: 'amd64', kubernetes: 'v1.36.1' };
    const contract = fs.readFileSync(path.join(env.KUBEATLAS_RELEASE_SOURCE_DIR, 'images/postgres-age/image.env'), 'utf8');
    requireValue(contract.includes(`POSTGRES_AGE_REPOSITORY=${DATABASE}\n`), 'database_repository');
    const databaseTag = contract.match(/^POSTGRES_AGE_TAG=([0-9]+\.[0-9]+[-.a-zA-Z0-9]+)$/m)?.[1];
    requireValue(databaseTag, 'database_tag');
    const version = inputs.tag.slice(1);
    const refs = { application: { repository: APP, tag: version, digest: inputs.appDigest },
      database: { repository: DATABASE, tag: databaseTag, digest: inputs.databaseDigest },
      chart: { repository: CHART, tag: version, digest: inputs.chartDigest } };
    phase('verify_artifacts');
    for (const [name, artifact] of Object.entries(refs)) {
      requireValue((await run('oras', ['resolve', `${artifact.repository}:${artifact.tag}`])).trim() === artifact.digest, 'tag_digest_drift');
      const index = await manifest(artifact.repository, artifact.digest, name);
      if (name === 'chart') {
        const layers = index.layers.filter(layer => layer.mediaType === 'application/vnd.cncf.helm.chart.content.v1.tar+gzip');
        requireValue(layers.length === 1, 'chart_layer_inventory');
        artifact.archiveDigest = layers[0].digest;
      } else {
        const children = index.manifests.filter(item => item.platform?.os === 'linux' && item.platform?.architecture === 'amd64');
        requireValue(children.length === 1, 'platform_inventory');
        artifact.platformDigest = children[0].digest;
        artifact.configDigest = (await manifest(artifact.repository, children[0].digest, `${name}-amd64`)).config.digest;
      }
    }
    evidence.artifacts = refs;
    const chartDirectory = path.join(root, 'chart'); fs.mkdirSync(chartDirectory);
    await helm(['pull', `oci://${CHART}@${inputs.chartDigest}`, '--destination', chartDirectory]);
    const archives = fs.readdirSync(chartDirectory).filter(name => name.endsWith('.tgz'));
    requireValue(archives.length === 1, 'chart_archive_inventory');
    const archive = path.join(chartDirectory, archives[0]);
    requireValue(fs.lstatSync(archive).isFile() && sha256(fs.readFileSync(archive)) === refs.chart.archiveDigest, 'chart_archive_digest');
    const metadata = await helm(['show', 'chart', archive]);
    const field = key => metadata.match(new RegExp(`^${key}:\\s*["']?([^"'\\s]+)["']?\\s*$`, 'm'))?.[1];
    requireValue(field('version') === version && field('appVersion') === version, 'chart_version');
    const values = await helm(['show', 'values', archive]);
    requireValue(values.match(/^    image:\s*(\S+)$/m)?.[1] === `${DATABASE}:${databaseTag}`, 'chart_database_contract');
    await helm(['lint', archive]);
    phase('install_operator');
    await createNamespace(OPERATOR_NS);
    await helm(['install', 'cnpg', 'cloudnative-pg', '--repo', 'https://cloudnative-pg.github.io/charts',
      '--version', '0.29.0', '--namespace', OPERATOR_NS, '--wait', '--timeout', '5m'], { timeout: 330000 });
    const operator = await json(['get', 'deployment', 'cnpg-cloudnative-pg', '-n', OPERATOR_NS, '-o', 'json']);
    requireValue(operator.spec.template.spec.containers[0].image === 'ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0', 'operator_version');
    evidence.operatorImage = operator.spec.template.spec.containers[0].image;
    await kube(['wait', '--for=condition=Established', 'crd/clusters.postgresql.cnpg.io', '--timeout=90s'], { timeout: 100000 });
    phase('install_tier2');
    await createNamespace(NS);
    const databaseImage = `${DATABASE}:${databaseTag}@${inputs.databaseDigest}`;
    await helm(['install', RELEASE, archive, '--namespace', NS, '--set-string', `image.digest=${inputs.appDigest}`,
      '--set', 'persistence.enabled=true', '--set', 'persistence.embedded.enabled=true', '--set-string', `persistence.embedded.image=${databaseImage}`,
      '--set', 'persistence.embedded.storageSize=1Gi', '--set', 'snapshots.enabled=true', '--wait', '--timeout', '10m'], { timeout: 630000 });
    const deployment = await json(['get', 'deployment', RELEASE, '-n', NS, '-o', 'json']);
    requireValue(deployment.spec.template.spec.containers.find(c => c.name === 'kubeatlas')?.image === `${APP}@${inputs.appDigest}`, 'application_pin');
    const pg = await json(['get', 'cluster.postgresql.cnpg.io', `${RELEASE}-pg`, '-n', NS, '-o', 'json']);
    requireValue(pg.spec.imageName === databaseImage && pg.status.readyInstances === 1, 'database_pin_or_readiness');
    const pvcs = await json(['get', 'pvc', '-n', NS, '-o', 'json']);
    requireValue(pvcs.items.length === 1 && pvcs.items[0].status.phase === 'Bound', 'pvc');
    evidence.pvcUID = pvcs.items[0].metadata.uid;
    const versions = await sql("SELECT json_build_object('postgres',current_setting('server_version'),'age',(SELECT extversion FROM pg_extension WHERE extname='age')); ");
    requireValue(versions.postgres.split(' ')[0] === databaseTag.split('-')[0] && versions.age, 'database_versions');
    evidence.databaseVersions = versions;
    phase('functional');
    await kube(['create', '-f', '-'], { input: JSON.stringify({ apiVersion: 'v1', kind: 'Secret',
      metadata: { name: 'evidence-sentinel', namespace: NS }, stringData: { value: token } }) });
    forward = forwardFactory();
    let ready = false;
    for (let n = 0; n < 60; n++) {
      try { await request('/readyz'); ready = true; break; }
      catch (error) { if (error.message !== 'http_transport') throw error; }
      requireValue(forward.alive(), 'port_forward_exited'); await sleep(1000);
    }
    requireValue(ready, 'port_forward_readiness');
    for (let sequence = 1; sequence <= 3; sequence++) {
      const started = now();
      const canary = await json(['apply', '-f', '-', '-o', 'json'], { input: JSON.stringify({ apiVersion: 'v1', kind: 'ConfigMap',
        metadata: { name: 'functional-canary', namespace: NS, annotations: { 'kubeatlas.io/functional-sequence': String(sequence) } },
        data: { sequence: String(sequence) } }) });
      const { uid, resourceVersion } = canary.metadata;
      requireValue(/^[0-9a-f-]{36}$/.test(uid) && /^[0-9]+$/.test(resourceVersion), 'canary_identity');
      let updateSeconds, historySeconds;
      while (true) {
        const response = JSON.parse((await request(`/api/v1/resources/${NS}/ConfigMap/functional-canary`, [200, 404])).body);
        updateSeconds = (now() - started) / 1000;
        requireValue(updateSeconds <= 30, 'update_deadline');
        if (response.resource?.resourceVersion === resourceVersion && response.resource.annotations?.['kubeatlas.io/functional-sequence'] === String(sequence)) break;
        await sleep(500);
      }
      while (true) {
        const counts = await sql(`SELECT json_build_object('matching',count(*)) FROM public.resource_events WHERE uid='${uid}' AND resource_version='${resourceVersion}';`);
        historySeconds = (now() - started) / 1000; requireValue(historySeconds <= 30, 'history_deadline');
        if (counts.matching > 0) break; await sleep(500);
      }
      const info = JSON.parse((await request('/api/v1/info')).body);
      requireValue(info.version === version && info.commit === inputs.commit && info.graphstore_version === 'v2', 'runtime_identity');
      const endpoints = {};
      for (const route of ['/healthz', '/readyz', '/', `/api/v1/graph?level=namespace&namespace=${NS}`,
        `/api/v1/diagnose?namespace=${NS}`, '/api/v1/snapshots', `/api/v1/snapshots/diff?from=5m&namespace=${NS}`,
        `/api/v1/resources/${NS}/Secret/evidence-sentinel`]) {
        const response = await request(route, route.includes('/Secret/') ? [200, 404] : [200]);
        if (route === '/') requireValue(response.body.includes('/assets/') && response.body.toLowerCase().includes('<html'), 'web_entry');
        endpoints[route] = { status: response.status, latencyMs: response.latencyMs };
      }
      const metrics = (await request('/metrics')).body;
      const metric = name => {
        const lines = metrics.split('\n').filter(line => line.startsWith(`${name} `));
        requireValue(lines.length === 1, 'metric_inventory');
        const value = Number(lines[0].split(' ')[1]); requireValue(Number.isFinite(value) && value >= 0, 'metric_value'); return value;
      };
      for (const name of ['kubeatlas_informer_synced', 'kubeatlas_kubernetes_api_reachable', 'kubeatlas_storage_reachable',
        'kubeatlas_storage_durable', 'kubeatlas_graph_observation_state{state="synced"}']) requireValue(metric(name) === 1, 'health_metric');
      for (const name of ['kubeatlas_snapshot_queue_drop_total', 'kubeatlas_snapshot_write_failed_total', 'kubeatlas_snapshot_queue_depth'])
        requireValue(metric(name) === 0, 'normal_load_error');
      metric('kubeatlas_go_memory_limit_bytes'); metric('kubeatlas_backup_status_available');
      const safety = await sql("SELECT json_build_object('history_payloads',(SELECT count(*) FROM public.resource_events WHERE data IS NOT NULL),'unsafe_secret_rows',(SELECT count(*) FROM public.resources WHERE (data->>'kind'='Secret' OR id ~ '(^|:)[^/]+/Secret/[^/]+$') AND (data - ARRAY['kind','name','namespace','annotations','clusterId']::text[]) <> '{}'::jsonb));");
      requireValue(safety.history_payloads === 0 && safety.unsafe_secret_rows === 0, 'database_safety');
      const pods = await json(['get', 'pods', '-n', NS, '-o', 'json']); const runtime = {};
      for (const [role, container] of [['application', 'kubeatlas'], ['database', 'postgres']]) {
        const selected = pods.items.filter(p => !p.metadata.deletionTimestamp && p.status.containerStatuses?.some(c => c.name === container));
        requireValue(selected.length === 1, 'pod_inventory');
        const pod = selected[0], state = pod.status.containerStatuses.find(c => c.name === container), artifact = refs[role];
        requireValue(state.ready && state.restartCount === 0 && pod.status.phase === 'Running' &&
          pod.status.conditions?.some(condition => condition.type === 'Ready' && condition.status === 'True') &&
          [...pod.status.containerStatuses, ...(pod.status.initContainerStatuses || [])].every(status =>
            status.restartCount === 0 && status.lastState?.terminated?.reason !== 'OOMKilled'), 'pod_health');
        requireValue([artifact.digest, artifact.platformDigest, artifact.configDigest].includes(state.imageID.split('@').pop()), 'runtime_image');
        if (evidence.samples.length) requireValue(pod.metadata.uid === evidence.samples[0].runtime[role].uid, 'pod_replacement');
        scan(await kube(['logs', '-n', NS, pod.metadata.name, '--all-containers=true', '--tail=200']));
        runtime[role] = { uid: pod.metadata.uid, imageID: state.imageID, ready: true, restarts: 0 };
      }
      evidence.samples.push({ sequence, resourceVersion, updateSeconds, historySeconds, runtime, endpoints,
        sentinelAbsent: true, databaseSafety: safety, healthPassed: true, zeroQueueAndWriteErrors: true });
      if (sequence < 3) await sleep(5000);
    }
    completed = true;
  } catch (error) {
    evidence.failure = { phase: evidence.phase, code: error instanceof GateError ? error.message : 'unexpected_failure' };
    evidence.failurePods = [];
    for (const resource of owned) {
      try {
        const pods = await json(['get', 'pods', '-n', resource.name, '-o', 'json']);
        for (const pod of pods.items) evidence.failurePods.push({ namespace: resource.name,
          name: pod.metadata.name, phase: pod.status.phase,
          containers: (pod.status.containerStatuses || []).map(container => ({ name: container.name,
            ready: container.ready, restarts: container.restartCount, imageID: container.imageID,
            waitingReason: container.state?.waiting?.reason, terminationReason: container.state?.terminated?.reason })) });
      } catch { /* Diagnostic failure must never erase the original gate failure. */ }
    }
  } finally {
    phase('cleanup');
    let cleaned = true;
    try { if (forward) await forward.stop(); } catch { cleaned = false; }
    for (const resource of owned.reverse()) {
      try {
        const current = await json(['get', 'namespace', resource.name, '-o', 'json']);
        requireValue(current.metadata.uid === resource.uid, 'cleanup_ownership');
        await kube(['delete', 'namespace', resource.name, '--wait=true', '--timeout=120s'], { timeout: 130000 });
      } catch { cleaned = false; }
    }
    fs.rmSync(root, { recursive: true, force: true });
    evidence.cleanupPassed = cleaned;
    evidence.status = completed && cleaned ? 'passed' : 'failed';
    evidence.phase = 'finished'; evidence.finishedAt = new Date().toISOString();
  }
  return evidence;
}

if (require.main === module) {
  audit().then(evidence => {
    fs.writeFileSync(process.env.KUBEATLAS_TIER2_EVIDENCE_FILE, `${JSON.stringify(evidence, null, 2)}\n`);
    console.log(`public Tier 2 artifact audit: ${evidence.status}${evidence.failure ? ` (${evidence.failure.phase}: ${evidence.failure.code})` : ''}`);
    process.exitCode = evidence.status === 'passed' ? 0 : 1;
  }).catch(() => { console.error('public Tier 2 artifact audit: setup or evidence write failed'); process.exitCode = 1; });
}
module.exports = { audit, CONTEXT, NS, OPERATOR_NS, sha256, runCommand };

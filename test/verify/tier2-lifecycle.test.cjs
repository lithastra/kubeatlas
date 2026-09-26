'use strict';

// Exercise the real Bash helpers with command doubles, never a live cluster.
const test = require('node:test');
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

function run(t, commands) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'tier2-forward-test-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  return spawnSync('bash', ['-c', `
    kubectl() { echo 'Unexpected live kubectl invocation' >&2; exit 99; }
    helm() { echo 'Unexpected live helm invocation' >&2; exit 99; }
    source "$1/tier2-lifecycle.sh"
    PF_LOG="$2/forward.log"
    kubeatlas_ready_deployment_pod() {
      [[ "$*" == 'custom-ns custom-release' ]] || return 1
      printf '%s\\n' 'selected-app'
    }
    ${commands}
  `, 'test', __dirname, directory], {
    encoding: 'utf8', timeout: 5000,
    env: { ...process.env, KUBEATLAS_NAMESPACE: 'custom-ns',
      KUBEATLAS_RELEASE: 'custom-release', KUBEATLAS_PF_PORT: '18081' },
  });
}

test('sourcing the lifecycle helpers never invokes cluster commands', t => {
  const result = run(t, 'printf "helpers loaded\\n"');
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /helpers loaded/);
});

test('forwards to the selected application Pod and cleans up the process', t => {
  const result = run(t, `
    kubectl() {
      [[ "$*" == 'port-forward -n custom-ns pod/selected-app 18081:8080' ]] || exit 98
      printf 'selected Pod forwarded\\n'
      exec sleep 20
    }
    curl() {
      [[ "$*" == '-fsS --max-time 1 http://127.0.0.1:18081/healthz' ]] || return 1
      # Ensure the child validated its arguments before the mock responds.
      for _ in $(seq 1 100); do
        if grep -q 'selected Pod forwarded' "$PF_LOG"; then return 0; fi
        sleep 0.01
      done
      return 1
    }
    start_port_forward
    pid=$PF_PID
    stop_port_forward
    [[ -z "$PF_PID" ]]
    ! kill -0 "$pid" 2>/dev/null
  `);
  assert.equal(result.status, 0, result.stderr);
});

for (const [name, selection, message] of [
  ['missing Ready Pod', "printf ''", /no Ready application Pod/],
  ['selection query failure', 'return 1', /could not select/],
]) {
  test(`fails closed on ${name} without starting a forward`, t => {
    const result = run(t, `kubeatlas_ready_deployment_pod() { ${selection}; }; start_port_forward`);
    assert.equal(result.status, 1, result.stderr);
    assert.match(result.stderr, message);
    assert.doesNotMatch(result.stderr, /Unexpected live/);
  });
}

for (const response of [0, 1]) {
  test(`reports an exited forward even when the HTTP probe returns ${response}`, t => {
    const result = run(t, `
      kubectl() { printf 'synthetic connection refused\\n' >&2; return 1; }
      curl() { wait "$PF_PID" || true; return ${response}; }
      sleep() { echo 'Unexpected retry after process exit' >&2; exit 97; }
      start_port_forward
    `);
    assert.equal(result.status, 1, result.stderr);
    assert.match(result.stderr, /synthetic connection refused/);
    assert.match(result.stderr, /port-forward to pod\/selected-app exited/);
    assert.doesNotMatch(result.stderr, /Unexpected retry/);
  });
}

test('retains the 60-probe budget and prints diagnostics on timeout', t => {
  const result = run(t, `
    kubectl() { printf 'synthetic forward stalled\\n' >&2; return 0; }
    kill() { return 0; }
    probes=0
    curl() { wait "$PF_PID" || true; probes=$((probes + 1)); return 1; }
    sleep() { :; }
    fail() { printf '%s\\nprobes=%s\\n' "$*" "$probes" >&2; exit 1; }
    start_port_forward
  `);
  assert.equal(result.status, 1, result.stderr);
  assert.match(result.stderr, /synthetic forward stalled/);
  assert.match(result.stderr, /API did not become reachable on :18081/);
  assert.match(result.stderr, /probes=60\n/);
});

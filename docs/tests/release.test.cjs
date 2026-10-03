// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

const assert = require('node:assert/strict');
const {readFileSync} = require('node:fs');
const path = require('node:path');
const {test} = require('node:test');

// Portable post-build checks: no cluster, credentials, or maintainer paths.
const docsRoot = path.resolve(__dirname, '..');
const read = (file) => readFileSync(path.join(docsRoot, file), 'utf8');
const page = (route = '') => read(path.join('build', route, 'index.html'));
const text = (html) => html.replace(/<[^>]*>/g, '').replace(/\s+/g, ' ');
const article = (html) => {
  const match = html.match(/<article[\s\S]*?<\/article>/);
  assert.ok(match, 'generated page must contain an article');
  return text(match[0]);
};

test('stable docs own the root; next and historical versions remain distinct', () => {
  const stable = page();
  assert.match(stable, /name="?docsearch:version"? content="?1\.6\.0(?:["\s/])/);
  assert.doesNotMatch(stable, /theme-doc-version-banner/);
  assert.match(article(stable), /v1\.6\.0 was released on September 26, 2026/);
  assert.doesNotMatch(article(stable), /release candidate|has not been published/i);
  assert.match(stable, /role=button[^>]*>1\.6\.0<\/a>/);
  assert.match(stable, /href=\/?next\/?>Next \(unreleased\)/);
  assert.match(text(page('next')), /This is unreleased documentation/);
  assert.match(text(page('1.5.2')), /no longer actively maintained/);
  assert.match(page('1.5.2'), /name="?docsearch:version"? content="?1\.5\.2(?:["\s/])/);
  // Preserve the original snapshot, including its historical 1.5.1 pin.
  assert.match(article(page('1.5.2/quick-start')), /--version 1\.5\.1/);
});

test('the v1.7 development contract is unreleased and does not change stable scope', () => {
  const contract = page('next/development/impact-analysis');
  assert.match(text(contract), /This is unreleased documentation/);
  assert.match(article(contract), /M0 design baseline, not a shipped feature/);
  assert.match(article(contract), /required future checks, not currently passing feature tests/);
  assert.match(article(contract), /independent authorization facet/);
  assert.match(article(contract), /effectivePermissions: not_evaluated/);
  assert.match(article(contract), /This M1 slice did not register a product endpoint/);
  assert.match(article(contract), /M2 observation collection: first slice/);
  assert.match(article(contract), /not durable history/);
  assert.match(article(contract), /pure M1 result still reports observation coverage as unknown/);
  assert.match(article(contract), /M2 observation-aware analysis: second slice/);
  assert.match(article(contract), /dynamic_source_scope_unverified/);
  assert.match(article(contract), /none_observed_incomplete/);
  assert.match(article(contract), /not a product endpoint/);
  assert.match(article(contract), /M2 storage and recording availability: third slice/);
  assert.match(article(contract), /retainedData: not_queried/);
  assert.match(article(contract), /not an earliest retained event/);
  assert.match(article(contract), /M2 cluster-attributed history reads: fourth slice/);
  assert.match(article(contract), /optional internal capability/);
  assert.match(article(contract), /Empty cluster selects only unlabelled rows/);
  assert.match(article(contract), /does not establish caller visibility/);
  assert.match(article(contract), /not acceptance against an existing cluster/);
  assert.match(article(contract), /M2 authorized v1 impact API: fifth slice/);
  assert.match(article(contract), /At most two impact requests per server/);
  assert.match(article(contract), /one combined 2 MiB cap/);
  assert.match(article(contract), /M2 CRD-source coverage: sixth slice/);
  assert.match(article(contract), /M2 Gatekeeper-source coverage: seventh slice/);
  assert.match(article(contract), /not proof that Gatekeeper is uninstalled/);
  assert.match(article(contract), /not the health of an otherwise connected watch/);
  assert.match(article(contract), /M2 API inventory and optional API presence: eighth slice/);
  assert.match(article(contract), /other_version_advertised/);
  assert.match(article(contract), /Core Secret endpoints are explicitly excluded/);
  assert.match(article(contract), /M2 CRD registration reconciliation: ninth slice/);
  assert.match(article(contract), /An old UID cannot stop a recreated CRD/);
  assert.match(article(contract), /Successor graph delivery therefore waits for the old delivery to finish/);
  assert.match(article(contract), /not missed-delete repair/);
  assert.match(article(contract), /M2 retained history metadata: tenth slice/);
  assert.match(article(contract), /A marker is a stored metadata record/);
  assert.match(article(contract), /Observed bounds do not prove a continuous retained interval/);
  assert.match(article(contract), /retentionEvidence/);
  assert.match(article(contract), /M3 server-backed Web analysis: first slice/);
  assert.match(article(contract), /not a product analysis fallback/);
  assert.match(article(contract), /Cluster selection changes close the old analysis/);
  assert.match(article(contract), /M3 CLI and captured reports: second slice/);
  assert.match(article(contract), /M3 cross-surface contract matrix: fourth slice/);
  assert.match(article(contract), /Synthetic client checks do not establish real-cluster watch reliability/);
  assert.match(article(contract), /Responsive checks should cover narrow and desktop layout bounds/);
  assert.match(article(contract), /M3 responsive analysis panels: fifth slice/);
  assert.match(article(contract), /Covered canvas controls are inert/);
  assert.match(article(contract), /M4 candidate gates remain separate/);
  assert.match(article(contract), /not real-cluster acceptance/);
  assert.match(article(page('next/cli-reference')), /kubectl atlas impact/);
  assert.match(article(page('next/cli-reference')), /KUBEATLAS_TOKEN/);
  assert.doesNotMatch(article(page('cli-reference')), /kubectl atlas impact/);
  assert.match(article(contract), /does not disable collection/);
  assert.match(article(contract), /healthy registered types are never a complete API inventory/);
  assert.match(article(page('next/api-reference')), /Impact analysis \(v1\.7, unreleased\)/);
  assert.doesNotMatch(article(page('api-reference')), /Impact analysis \(v1\.7, unreleased\)/);
  assert.match(article(page('next/roadmap')), /v1\.7\.0/);
  assert.doesNotMatch(article(page('roadmap')), /v1\.7\.0/);
  assert.doesNotMatch(page(), /href=["']?[^\s>"']*development\/impact-analysis/);
});

for (const prefix of ['', 'next/']) {
  test(`${prefix || 'stable/'} fresh-install instructions use v1.6.0`, () => {
    for (const route of [
      'quick-start', 'installation/helm', 'installation/cert-manager',
      'installation/eks', 'installation/openshift', 'installation/ingress-alb',
      'installation/ingress-nginx-f5', 'installation/ingress-traefik',
      'installation/otel-integration', 'concepts/snapshots',
    ]) {
      const content = article(page(prefix + route));
      assert.match(content, /--version 1\.6\.0/, route);
      assert.doesNotMatch(content, /--version 1\.5\.2/, route);
    }
  });

  test(`${prefix || 'stable/'} persistence keeps staged upgrade prerequisites`, () => {
    const content = article(page(prefix + 'installation/persistence'));
    const start = content.indexOf('This historical first step');
    const end = content.indexOf('CloudNativePG 1.24 and 1.30', start);
    assert.ok(start >= 0 && end > start, 'historical upgrade section must exist');
    const oldUpgrade = content.slice(start, end);
    assert.match(oldUpgrade, /--version 0\.22\.1/);
    assert.match(oldUpgrade, /--version 1\.5\.2/);
    assert.doesNotMatch(oldUpgrade, /--version 1\.6\.0/);
    const commands = [...page(prefix + 'installation/persistence')
      .matchAll(/<pre[\s\S]*?<\/pre>/g)].map((match) => text(match[0]));
    const fresh = commands.filter((command) =>
      command.includes('helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas'));
    assert.equal(fresh.length, 2, 'embedded and BYO examples must remain pinned');
    for (const command of fresh) assert.match(command, /--version 1\.6\.0/);
    assert.match(content, /Security upgrade to v1\.5\.2/);
  });

  test(`${prefix || 'stable/'} release evidence preserves identity and limits`, () => {
    const content = article(page(prefix + 'release-process'));
    for (const value of [
      '69609e320045cbd943185dfa7900c3302359999e',
      'sha256:ff185a897c10f244de6b014a868af4fecaa1f15c300c1de9a7b58a62a3b338c8',
      'sha256:412ae56754e71aac96e8cb6327f70782c50bd4aeff75ff0ddf32eb4ee7400936',
      'sha256:1ba4fe34276337f70d5a5d160dea1a3f225c9d3950b6e869691219a7c528fce3',
      'No 72-hour endurance test, dedicated performance acceptance',
      '1.0.1 source-only delivery',
      'not a v1.6.0 compatibility result',
    ]) assert.ok(content.includes(value), value);
    assert.doesNotMatch(content, /publication pending|current public release is still v1\.5\.2/);
  });

  test(`${prefix || 'stable/'} renamed sections preserve inbound anchors`, () => {
    for (const [route, ids] of [
      ['release-process', ['verifying-a-v16-core-candidate']],
      ['concepts/performance', ['planned-v16-release-gate']],
      ['roadmap', ['v16-planned--production-operability', 'committed-scope', 'v160-release-gates']],
    ]) {
      const html = page(prefix + route);
      for (const id of ids) {
        assert.equal([...html.matchAll(new RegExp(' id="?' + id + '(?:["\\s>])', 'g'))].length,
          1, `${route}#${id} must resolve exactly once`);
      }
    }
  });
}

test('version-qualified stable URLs have temporary, path-preserving edge aliases', () => {
  const source = read('static/_redirects');
  assert.equal(read('build/_redirects'), source);
  const rules = source.split('\n').map((line) => line.trim())
    .filter((line) => line && !line.startsWith('#'));
  assert.deepEqual(rules, ['/1.6.0 / 302', '/1.6.0/* /:splat 302']);
  // A 301 would leave historical links pointing at a newer stable version later.
  assert.doesNotMatch(source, /301|\/1\.5\.2/);
});

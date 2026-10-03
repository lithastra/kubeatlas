import { impactCaptureJSON } from '../api/impactCapture';
import type { ImpactFacet, ImpactResponse } from '../api/impactTypes';

export type ImpactExportFormat = 'json' | 'html';

function escape(value: unknown): string {
  return String(value).replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]!);
}

function details(title: string, value: unknown): string {
  return `<details><summary>${escape(title)}</summary><pre>${escape(JSON.stringify(value, null, 2))}</pre></details>`;
}

function facetHTML(title: string, facet: ImpactFacet): string {
  return `<section><h2>${escape(title)}</h2>
<p>${facet.counts.total} observed · ${facet.counts.direct} direct · ${facet.counts.indirect} indirect</p>
<p>Lower bound: ${facet.counts.lowerBound} · Truncated: ${facet.truncated} · Observation: ${escape(facet.observationCoverage)}</p>
<p>Completed modeled traversal is not proof of complete observation or safe deletion. Paths are representative, not exhaustive.</p>
${facet.truncated ? `<p class="warning">Truncated: ${escape(facet.truncationReasons.join(', '))}</p>` : ''}
${facet.resources.map((match) => `<details><summary>${escape(match.resource.id)} · ${match.depth === 1 ? 'Direct' : `Indirect (${match.depth} hops)`}</summary>
<p>UID: ${escape(match.resource.uid || 'unknown / reference only')} · Resource version: ${escape(match.resource.resourceVersion || 'unknown')} · Reference only: ${match.resource.referenceOnly}</p>
<ol>${match.path.map((step) => `<li>${escape(step.traversalFrom)} → ${escape(step.traversalTo)}<br>
Stored: ${escape(step.from)} → ${escape(step.to)} · ${escape(step.type)}
${step.ownerUIDStatus ? ` · Owner UID: ${escape(step.ownerUIDStatus)}` : ''}${step.bindingScope ? ` · Binding scope: ${escape(step.bindingScope)} ${escape(step.bindingNamespace || '')}` : ''}</li>`).join('')}</ol></details>`).join('')}
${details('Traversal notices', facet.notices)}</section>`;
}

export function renderImpactExport(response: ImpactResponse, format: ImpactExportFormat): { content: string; mime: string; filename: string } {
  const raw = impactCaptureJSON(response); // Verified immutable capture, not a reconstruction.
  const stamp = response.generatedAt.replace(/[^a-zA-Z0-9]/g, '').slice(0,40);
  const filename = `kubeatlas-impact-${stamp}.${format}`;
  if (format === 'json') return { content: raw, mime: 'application/json;charset=utf-8', filename };
  const a = response.analysis;
  const observation = a.observation!.ordinary;
  const closedEmpty = a.modeledTraversalComplete && !a.truncated && observation.typeScopeClosed && observation.stableWindow && observation.emptyResultAssessment === 'none_found_in_analyzed_scope';
  const empty = a.resources.length ? '' : closedEmpty ? `No ${a.relation} found in the analyzed scope; not a safety guarantee.` : `No ${a.relation} observed; analysis incomplete.`;
  const content = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'">
<meta name="referrer" content="no-referrer"><title>KubeAtlas captured impact analysis</title>
<style>
:root{font-family:system-ui,sans-serif;color:#17243a;background:#edf1f5;color-scheme:light}body{margin:0;padding:24px}main{max-width:1050px;margin:auto}header,section{background:white;border:1px solid #d6dfe8;border-radius:10px;padding:24px;margin-bottom:20px;overflow-wrap:anywhere}h1{font-size:clamp(1.5rem,4vw,2.2rem)}h2{font-size:1.2rem}p,li{line-height:1.6}.warning{border-left:4px solid #ad651a;padding:12px 16px;background:#fff6e8}summary{cursor:pointer;font-weight:600;padding:10px 0}pre{white-space:pre-wrap;overflow-wrap:anywhere;font-size:.85rem;line-height:1.55}li{margin-bottom:12px}footer{color:#52647b;font-size:.85rem}@media(max-width:600px){body{padding:12px}header,section{padding:16px}}@media print{body{background:white;padding:0}}
</style></head><body><main>
<header><p>Captured analysis · not a live view</p><h1>KubeAtlas impact analysis</h1>
<p>${escape(a.root.id)} · ${escape(a.relation)} · Depth ${a.maxDepth} · Limit ${a.limit}</p>
<p>Cluster: ${escape(response.scope.clusterId || 'standalone')} · All namespaces in selected cluster</p>
<p>Captured: ${escape(response.generatedAt)} · KubeAtlas ${escape(response.kubeatlasVersion)} · Schema ${escape(response.schemaVersion)}</p>
<p>Root UID: ${escape(a.root.uid || 'unknown / reference only')} · Resource version: ${escape(a.root.resourceVersion || 'unknown')} · Reference only: ${a.root.referenceOnly}</p>
<p class="warning">${escape(response.sharingWarning)}</p>
<p>Canvas filters do not restrict this analysis. Counts exclude the root. Authorization associations are separate.</p>
${empty ? `<p class="warning">${escape(empty)}</p>` : ''}</header>
${facetHTML('Ordinary relationships', a)}
<section><h2>Reference declarations</h2><p>Not Secret values, observed application reloads, or proof that a change is harmless.</p>${details('Reference field evidence', a.referenceEvidence)}</section>
${a.authorization.applicable ? facetHTML('Authorization associations (separate counts)', a.authorization) : `<section><h2>Authorization associations</h2><p>Not applicable: ${escape(a.authorization.reason || 'unsupported root')}</p></section>`}
<section><h2>Observation, authorization and history evidence</h2>
<p>Authorization associations are not effective permissions or credential-use proof. Unknown, partial, stale, stopped and unsupported evidence remain limitations.</p>
<p>Zero timestamps mean none observed / unknown. Retained bounds do not prove continuous history. Marker records are not backups or verified recovery points.</p>
${details('Independent authorization evidence', a.authorization)}${details('Observation coverage', a.observation)}${details('Cached storage and history', a.availability)}</section>
<section><details><summary>Complete captured JSON</summary><p>The original response used by the Web view. No new query was made for this export. Numeric and timestamp text is preserved here.</p><pre id="captured-json">${escape(raw)}</pre></details></section>
<footer>Self-contained report. No scripts or external resources. Share only with authorized recipients.</footer>
</main></body></html>`;
  return { content, mime: 'text/html;charset=utf-8', filename };
}

// Synchronous user-gesture download: no fetch, upload, worker, or storage write.
// The browser owns destination/permissions and may still block or cancel it.
export function downloadImpactCapture(response: ImpactResponse, format: ImpactExportFormat): void {
  const report = renderImpactExport(response, format);
  const url = URL.createObjectURL(new Blob([report.content], { type: report.mime }));
  const anchor = document.createElement('a');
  try {
    anchor.href = url;
    anchor.download = report.filename;
    anchor.rel = 'noopener';
    document.body.appendChild(anchor);
    anchor.click();
  } finally {
    anchor.remove();
    // Keep the blob alive long enough for browsers to start the download.
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
}

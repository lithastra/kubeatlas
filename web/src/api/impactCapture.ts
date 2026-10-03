import { ApiError } from './client';
import schema from './impactSchema.json';
import type { ImpactResponse } from './impactTypes';

export const impactMaxBytes = 2 << 20;
export const impactSharingWarning = 'Contains sensitive cluster topology. Review access and sharing permissions before exporting. Relationships are observations, not a safety or outage guarantee.';

interface WireSchema {
  type: string;
  nullable?: boolean;
  items?: WireSchema;
  properties?: Record<string, WireSchema>;
  required?: string[];
}

// Not browser storage: captures become collectible with their query results.
// Never reconstruct an export from the smaller display-only TypeScript type.
const captures = new WeakMap<ImpactResponse, string>();

function rejectDuplicateKeys(raw: string): void {
  // JSON.parse has already checked syntax. Check every object key, including
  // escaped spellings, so overwritten fields cannot hide unvalidated payloads
  // in the original text that we will export.
  const stack: Array<{ keys: Set<string> | null; expectsKey: boolean }> = [];
  for (const [token] of raw.matchAll(/"(?:\\.|[^"\\])*"|[{}\[\],:]|[^{}\[\],:\s"]+/g)) {
    if (token === '{' || token === '[') stack.push({ keys: token === '{' ? new Set() : null, expectsKey: token === '{' });
    else if (token === '}' || token === ']') stack.pop();
    else {
      const frame = stack.at(-1);
      if (!frame?.keys) continue;
      if (token === ',') frame.expectsKey = true;
      else if (frame.expectsKey && token.startsWith('"')) {
        const key: string = JSON.parse(token);
        if (frame.keys.has(key)) throw new Error('Duplicate impact response field');
        frame.keys.add(key);
        frame.expectsKey = false;
      }
    }
  }
}

function matchesSchema(value: unknown, shape: WireSchema): boolean {
  if (value === null) return shape.nullable === true;
  switch (shape.type) {
    case 'string': return typeof value === 'string';
    case 'boolean': return typeof value === 'boolean';
    case 'integer': return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value);
    case 'array': return Array.isArray(value) && !!shape.items && value.every((item) => matchesSchema(item, shape.items!));
    case 'object': {
      if (typeof value !== 'object' || Array.isArray(value)) return false;
      const record = value as Record<string, unknown>;
      return (shape.required ?? []).every((key) => Object.hasOwn(record, key)) &&
        Object.entries(record).every(([key, child]) => Object.hasOwn(shape.properties ?? {}, key) && matchesSchema(child, shape.properties![key]));
    }
    default: return false;
  }
}

function freeze(value: unknown): void {
  if (value && typeof value === 'object') {
    Object.values(value).forEach(freeze);
    Object.freeze(value);
  }
}

// The checked-in schema is locked to the Go API DTO by a portable Go test.
// Unknown fields fail closed, including nested raw/data/stringData payloads.
export function parseImpactCapture(raw: string): ImpactResponse {
  if (new TextEncoder().encode(raw).byteLength > impactMaxBytes) throw new Error('Impact capture exceeds its size limit');
  const value: unknown = JSON.parse(raw);
  rejectDuplicateKeys(raw);
  if (!matchesSchema(value, schema)) throw new Error('Unsupported impact response schema');
  const r = value as ImpactResponse;
  const a = r.analysis;
  if (r.schemaVersion !== '1' || !r.kubeatlasVersion || !Number.isFinite(Date.parse(r.generatedAt)) ||
      r.sharingWarning !== impactSharingWarning || !a || !a.observation || !a.availability ||
      !Array.isArray(a.resources) || !Array.isArray(a.notices) || !Array.isArray(a.referenceEvidence) ||
      !Array.isArray(a.authorization.resources) || !Array.isArray(a.authorization.notices) ||
      !Array.isArray(a.truncationReasons) || !Array.isArray(a.authorization.truncationReasons) ||
      !Array.isArray(a.observation.ordinary.reasons) || !Array.isArray(a.observation.authorization.reasons) ||
      !Array.isArray(a.availability.evidence.history.reasons) ||
      !a.resources.every((match) => Array.isArray(match.path)) || !a.authorization.resources.every((match) => Array.isArray(match.path)) ||
      !a.referenceEvidence.every((ref) => Array.isArray(ref.uses))) {
    throw new Error('Incomplete impact response');
  }
  // All scoped metadata, even records not displayed by the Web panel, must
  // belong to the same capture. Canonical paths cannot point at another scope.
  const cluster = r.scope.clusterId;
  if (r.scope.mode !== (cluster ? 'federated' : 'single') || r.scope.namespaceScope !== 'all_namespaces_in_selected_cluster') {
    throw new Error('Impact scope mismatch');
  }
  const checkScope = (item: unknown): void => {
    if (!item || typeof item !== 'object') return;
    const record = item as Record<string, unknown>;
    if (Object.hasOwn(record, 'clusterId') && record.clusterId !== cluster) throw new Error('Impact scope mismatch');
    if (Object.hasOwn(record, 'id')) {
      const id = `${cluster ? `${cluster}:` : ''}${record.namespace}/${record.kind}/${record.name}`;
      if (record.id !== id || !record.kind || !record.name || (cluster && record.clusterId !== cluster)) throw new Error('Impact identity mismatch');
      if (record.kind === 'Secret' && (!record.referenceOnly || record.uid || record.resourceVersion)) throw new Error('Invalid reference-only Secret');
    }
    Object.values(item).forEach(checkScope);
  };
  checkScope(r);
  if (!['dependents', 'dependencies'].includes(a.relation) || a.maxDepth < 1 || a.maxDepth > 10 || a.limit < 1 || a.limit > 1000) {
    throw new Error('Invalid impact bounds');
  }
  if (a.authorization.maxDepth !== a.maxDepth || a.authorization.limit !== a.limit) throw new Error('Inconsistent impact bounds');
  for (const facet of [a, a.authorization]) {
    const ids = new Set([a.root.id, ...facet.resources.map((match) => match.resource.id)]);
    if (facet.resources.length > a.limit || ids.size !== facet.resources.length + 1 || facet.counts.total !== facet.resources.length ||
        facet.counts.direct !== facet.resources.filter((match) => match.depth === 1).length || facet.counts.indirect !== facet.counts.total - facet.counts.direct) {
      throw new Error('Inconsistent impact counts');
    }
    for (const match of facet.resources) {
      if (match.depth < 1 || match.depth > a.maxDepth || match.path.length !== match.depth) throw new Error('Invalid impact path');
      let last = a.root.id;
      for (const step of match.path) {
        if (!ids.has(step.from) || !ids.has(step.to) || step.traversalFrom !== last ||
            !((step.from === step.traversalFrom && step.to === step.traversalTo) || (step.to === step.traversalFrom && step.from === step.traversalTo))) {
          throw new Error('Invalid impact path');
        }
        last = step.traversalTo;
      }
      if (last !== match.resource.id) throw new Error('Invalid impact path');
    }
  }
  const ordinaryIDs = new Set([a.root.id, ...a.resources.map((match) => match.resource.id)]);
  if (a.referenceEvidence.some((ref) => !ordinaryIDs.has(ref.from) || !ordinaryIDs.has(ref.to))) throw new Error('Invalid impact reference');
  freeze(r);
  captures.set(r, raw);
  return r;
}

export function isImpactCapture(r: ImpactResponse): boolean { return captures.has(r); }

export function impactCaptureJSON(r: ImpactResponse): string {
  const raw = captures.get(r);
  if (raw === undefined) throw new Error('No verified impact capture');
  return raw;
}

// This impact-only reader does not alter legacy client behavior. It keeps exact
// UTF-8 JSON (including integer lexemes and timestamps), with a decoded byte cap.
export async function fetchImpactCapture(url: string, options: { signal?: AbortSignal } = {}): Promise<ImpactResponse> {
  const controller = new AbortController();
  const abort = () => controller.abort();
  const timeout = setTimeout(abort, 15_000);
  options.signal?.addEventListener('abort', abort, { once: true });
  if (options.signal?.aborted) abort();
  try {
    const response = await fetch(url, { method: 'GET', headers: { Accept: 'application/json' },
      signal: controller.signal, redirect: 'error', cache: 'no-store', credentials: 'same-origin' });
    if (response.status !== 200) throw new ApiError(response.status, 'impact_failed', 'Impact request failed');
    if (response.headers.get('content-type')?.split(';')[0].trim() !== 'application/json' || !response.body) {
      throw new Error('Invalid impact response type');
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true });
    let bytes = 0;
    let raw = '';
    try {
      for (;;) {
        const part = await reader.read();
        if (part.done) break;
        bytes += part.value.byteLength;
        if (bytes > impactMaxBytes) throw new Error('Impact response exceeds size limit');
        raw += decoder.decode(part.value, { stream: true });
      }
      raw += decoder.decode();
      return parseImpactCapture(raw);
    } finally {
      try { await reader.cancel(); } catch { /* Never relay a stream error body. */ }
      reader.releaseLock();
    }
  } finally {
    clearTimeout(timeout);
    options.signal?.removeEventListener('abort', abort);
    controller.abort();
  }
}

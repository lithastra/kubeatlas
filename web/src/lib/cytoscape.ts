import cytoscape, { type Core, type ElementDefinition } from 'cytoscape';
// Import as namespace + unwrap defensively. In dev builds Vite hands
// us the default export; in some production builds the same line
// returns the namespace object, leaving `dagre` as undefined and
// blowing up `cytoscape.use(dagre)` at registration time. The `??`
// covers both shapes.
import * as cytoscapeDagre from 'cytoscape-dagre';

import type { View, ViewEdge, ViewNode } from '../api/types';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const dagre: cytoscape.Ext = (cytoscapeDagre as any).default ?? (cytoscapeDagre as unknown as cytoscape.Ext);

// Register the dagre layout once at module import time. Subsequent
// imports are no-ops.
let dagreRegistered = false;
function ensureDagre() {
  if (dagreRegistered) return;
  cytoscape.use(dagre);
  dagreRegistered = true;
}

// Visual constants. Centralised here so a UX tweak doesn't require
// hunting through React components — the spec is explicit about not
// inlining cytoscape configuration in the component layer.

const layout = {
  name: 'dagre',
  rankDir: 'TB',
  nodeSep: 50,
  rankSep: 80,
  fit: true,
  padding: 24,
};

// Per-kind colour palette. Picked to be color-blind friendly: blues
// for compute, greens for config, oranges/reds for routing, purple
// for service identity. No rainbow assignments.
//
// Each entry becomes a cytoscape style rule keyed on the node's
// `data(kind)` selector — see the stylesheet below. This avoids the
// function-style style accessors (which are difficult to type).
const kindColor: Record<string, string> = {
  Deployment: '#1976d2',
  StatefulSet: '#1565c0',
  DaemonSet: '#0d47a1',
  Job: '#283593',
  CronJob: '#1a237e',
  ReplicaSet: '#42a5f5',
  Pod: '#90caf9',
  Service: '#7e57c2',
  ConfigMap: '#43a047',
  Secret: '#2e7d32',
  PersistentVolumeClaim: '#558b2f',
  ServiceAccount: '#9c27b0',
  Ingress: '#ef6c00',
  Gateway: '#e65100',
  HTTPRoute: '#fb8c00',
  Namespace: '#5d4037',
};
const defaultKindColor = '#9e9e9e';

// Build per-kind style rules upfront. Cytoscape accepts an array of
// {selector, style} objects of arbitrary length.
const kindRules = Object.entries(kindColor).map(([kind, color]) => ({
  selector: `node[kind = "${kind}"]`,
  style: {
    'background-color': color,
    'text-outline-color': color,
  },
}));

const stylesheet = [
  {
    selector: 'node',
    style: {
      'background-color': defaultKindColor,
      'text-outline-color': defaultKindColor,
      label: 'data(label)',
      color: '#fff',
      'text-valign': 'center',
      'text-halign': 'center',
      'font-size': 11,
      'text-outline-width': 2,
      width: 90,
      height: 36,
      shape: 'round-rectangle',
    },
  },
  ...kindRules,
  {
    selector: 'node[type = "aggregated"]',
    style: {
      shape: 'round-tag',
      width: 110,
      'border-width': 1,
      'border-color': '#fff',
    },
  },
  {
    selector: 'edge',
    style: {
      width: 1.5,
      'line-color': '#9e9e9e',
      'curve-style': 'bezier',
      'target-arrow-shape': 'triangle',
      'target-arrow-color': '#9e9e9e',
      'arrow-scale': 0.8,
      label: 'data(typeLabel)',
      'font-size': 9,
      'text-background-color': '#fff',
      'text-background-opacity': 0.9,
      'text-background-padding': 2,
      color: '#555',
    },
  },
];

// Performance flags pulled out so future tuning is one place. These
// are the spec-recommended defaults for the 1000-node target the
// P1-T14 perf check measures against.
const perfOptions = {
  textureOnViewport: true,
  hideEdgesOnViewport: true,
  hideLabelsOnViewport: true,
  pixelRatio: 1,
  wheelSensitivity: 0.2,
};

// elementsFromView turns a server-side aggregated View into the
// {nodes, edges} shape Cytoscape consumes.
//
// Defensive nullish coalescing: Go's json/encoding serialises an
// empty slice as `null` unless the field was explicitly initialised
// to a non-nil zero-length slice. Cluster-level views with no
// cross-namespace edges hit this; without `?? []` the for-of loop
// throws "edges is not iterable" and unmounts the page.
export function elementsFromView(view: View): ElementDefinition[] {
  const elements: ElementDefinition[] = [];
  for (const n of view.nodes ?? []) {
    elements.push({
      group: 'nodes',
      data: {
        id: n.id,
        kind: n.kind ?? '',
        label: nodeLabel(n),
        type: n.type,
      },
    });
  }
  for (const e of view.edges ?? []) {
    elements.push({
      group: 'edges',
      data: {
        id: edgeID(e),
        source: e.from,
        target: e.to,
        typeLabel: e.type ?? '',
      },
    });
  }
  return elements;
}

function nodeLabel(n: ViewNode): string {
  if (n.label) return n.label;
  if (n.name) return n.name;
  return n.id;
}

function edgeID(e: ViewEdge): string {
  return `${e.from}->${e.to}/${e.type ?? ''}`;
}

// createCytoscape boots a cytoscape instance into the given container
// and applies the dagre layout to the initial elements. Callers own
// the returned Core and should call .destroy() on unmount.
export function createCytoscape(container: HTMLElement, view: View): Core {
  ensureDagre();
  const cy = cytoscape({
    container,
    elements: elementsFromView(view),
    // The cytoscape Stylesheet type is a complex union; our rules are
    // pure CSS-shape but TS can't narrow that without verbose typing.
    style: stylesheet as unknown as cytoscape.StylesheetCSS[],
    ...perfOptions,
  });
  cy.layout(layout).run();
  return cy;
}

// updateCytoscape applies a new view to an existing cytoscape
// instance without destroying it. cy.json({elements}) does a
// structural diff that's much cheaper than destroy + recreate; the
// spec is explicit about preferring this over recreate-on-update.
export function updateCytoscape(cy: Core, view: View): void {
  cy.json({ elements: elementsFromView(view) });
  cy.layout(layout).run();
}

// Re-export the layout constant so future tests can assert it
// without reaching into the file's lexical scope.
export const dagreLayout = layout;

import cytoscape, { type Core, type ElementDefinition } from 'cytoscape';
// Vite dev hands us the default export; some production bundles
// return the namespace object — `??` covers both.
import * as cytoscapeDagre from 'cytoscape-dagre';

import type { View, ViewEdge, ViewNode } from '../api/types';
import {
  themePalettes,
  DEFAULT_THEME,
  type AtlasEdgePalette,
  type AtlasPalette,
  type AtlasThemeName,
} from '../theme';

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

// ----- Cartography node families ----------------------------------------

export type NodeFamily =
  | 'workload'
  | 'configuration'
  | 'identity'
  | 'network'
  | 'storage'
  | 'custom';

// Canonical K8s Kind → family map. Anything outside this list falls
// into the CRD-fallback rules below (classifyByName).
const FAMILY_BY_KIND: Record<string, NodeFamily> = {
  Pod: 'workload',
  Deployment: 'workload',
  ReplicaSet: 'workload',
  StatefulSet: 'workload',
  DaemonSet: 'workload',
  Job: 'workload',
  CronJob: 'workload',
  Node: 'workload',
  ConfigMap: 'configuration',
  Secret: 'configuration',
  ServiceAccount: 'identity',
  Role: 'identity',
  ClusterRole: 'identity',
  RoleBinding: 'identity',
  ClusterRoleBinding: 'identity',
  Service: 'network',
  Ingress: 'network',
  Gateway: 'network',
  HTTPRoute: 'network',
  NetworkPolicy: 'network',
  EndpointSlice: 'network',
  PersistentVolume: 'storage',
  PersistentVolumeClaim: 'storage',
  StorageClass: 'storage',
};

/**
 * classifyKind picks a NodeFamily for an arbitrary GVK. Canonical
 * Kinds resolve directly; unknown Kinds (CRDs) walk the deterministic
 * fallback ladder from the design spec.
 */
export function classifyKind(kind: string): NodeFamily {
  const direct = FAMILY_BY_KIND[kind];
  if (direct) return direct;
  return classifyByName(kind);
}

/**
 * True when the family is inferred from a Kind name pattern, not
 * the canonical table. The UI renders a small ◇ mark on these so
 * operators can tell auto-categorised nodes from canonical ones.
 */
export function isInferredKind(kind: string): boolean {
  return FAMILY_BY_KIND[kind] == null;
}

function classifyByName(kind: string): NodeFamily {
  // Rule order matters; first match wins.
  if (/(Binding|RoleBinding)$/.test(kind)) return 'identity';
  if (/Policy/i.test(kind)) return 'network';
  if (/(Route|Gateway|Listener|VirtualService|Endpoint.*)$/.test(kind)) return 'network';
  if (/(Set|Workload|Cluster|Job|Workflow)$/.test(kind)) return 'workload';
  if (/(Volume|Claim|Storage)$/.test(kind)) return 'storage';
  if (/Secret|Cert|Credential/i.test(kind)) return 'configuration';
  if (/Config|Map|Profile/i.test(kind)) return 'configuration';
  return 'custom';
}

// ----- Cartography edge encoding ----------------------------------------

export type EdgeWeight = 'heavy' | 'medium' | 'light' | 'hairline';
export type EdgeDash = 'solid' | 'longDash' | 'shortDash' | 'dotted' | 'longShort';
export type EdgeArrow = 'filled-tri' | 'open' | 'diamond' | 'bar-end' | 'double-tri';
export type EdgeDomain = keyof AtlasEdgePalette;

export interface EdgeStyleSpec {
  weight: EdgeWeight;
  dash: EdgeDash;
  domain: EdgeDomain;
  arrow: EdgeArrow;
  flow?: boolean;
}

// The 14 edge-type table. Adding a new edge type means: add an
// entry here, the four channels will drive the generated stylesheet.
const EDGE_STYLES: Record<string, EdgeStyleSpec> = {
  OWNS: { weight: 'heavy', dash: 'solid', domain: 'structural', arrow: 'filled-tri' },
  ATTACHED_TO: { weight: 'medium', dash: 'solid', domain: 'traffic', arrow: 'filled-tri' },
  MOUNTS_VOLUME: { weight: 'medium', dash: 'longDash', domain: 'storage', arrow: 'open' },
  USES_CONFIGMAP: { weight: 'medium', dash: 'longDash', domain: 'config', arrow: 'open' },
  USES_SECRET: { weight: 'medium', dash: 'longDash', domain: 'secret', arrow: 'open' },
  USES_SERVICEACCOUNT: { weight: 'medium', dash: 'longDash', domain: 'identity', arrow: 'open' },
  IMAGE_PULL_SECRET: { weight: 'medium', dash: 'longDash', domain: 'secret', arrow: 'open' },
  SELECTS: { weight: 'light', dash: 'shortDash', domain: 'structural', arrow: 'bar-end' },
  ROUTES_TO: { weight: 'medium', dash: 'longDash', domain: 'traffic', arrow: 'filled-tri', flow: true },
  RBAC_BINDS: { weight: 'medium', dash: 'longDash', domain: 'identity', arrow: 'diamond' },
  BINDS_SUBJECT: { weight: 'medium', dash: 'longDash', domain: 'identity', arrow: 'diamond' },
  BINDS_ROLE: { weight: 'medium', dash: 'longDash', domain: 'identity', arrow: 'diamond' },
  POLICY_TARGET: { weight: 'light', dash: 'dotted', domain: 'policy', arrow: 'diamond' },
  SELECTS_NP: { weight: 'light', dash: 'dotted', domain: 'policy', arrow: 'diamond' },
  ALLOWS_FROM: { weight: 'light', dash: 'dotted', domain: 'policy', arrow: 'diamond' },
  ALLOWS_TO: { weight: 'light', dash: 'dotted', domain: 'policy', arrow: 'diamond' },
  SCALES: { weight: 'light', dash: 'dotted', domain: 'policy', arrow: 'diamond' },
  PROTECTS: { weight: 'light', dash: 'dotted', domain: 'policy', arrow: 'diamond' },
  BINDS_PLATFORM_IDENTITY: { weight: 'medium', dash: 'longShort', domain: 'federation', arrow: 'double-tri' },
  CRD_REF: { weight: 'light', dash: 'longDash', domain: 'structural', arrow: 'open' },
};

const DEFAULT_EDGE_STYLE: EdgeStyleSpec = {
  weight: 'medium',
  dash: 'longDash',
  domain: 'structural',
  arrow: 'open',
};

export function edgeStyleFor(edgeType: string | undefined): EdgeStyleSpec {
  if (!edgeType) return DEFAULT_EDGE_STYLE;
  return EDGE_STYLES[edgeType] ?? DEFAULT_EDGE_STYLE;
}

const WEIGHT_PX: Record<EdgeWeight, number> = {
  heavy: 2.5,
  medium: 1.75,
  light: 1.25,
  hairline: 0.75,
};

const DASH_PATTERN: Record<EdgeDash, number[]> = {
  solid: [],
  longDash: [8, 4],
  shortDash: [4, 4],
  dotted: [2, 3],
  longShort: [8, 4, 2, 4],
};

const ARROW_CY_SHAPE: Record<EdgeArrow, string> = {
  'filled-tri': 'triangle',
  open: 'triangle-tee',
  diamond: 'diamond',
  'bar-end': 'tee',
  'double-tri': 'triangle-backcurve',
};

// ----- Stylesheet builder -----------------------------------------------

const layout = {
  name: 'dagre',
  rankDir: 'TB',
  nodeSep: 50,
  rankSep: 80,
  fit: true,
  padding: 24,
};

/**
 * buildAtlasStylesheet emits the full Cytoscape stylesheet for a
 * given palette. Re-call it on theme change and pass the result to
 * cy.style().fromJson(...).update() so node colours and edge domain
 * colours swap live without recreating the graph.
 *
 * Style rules cascade in declaration order — later rules win for
 * the same property. The order here is: base node → per-family
 * overrides → per-state overrides → interaction overrides → base
 * edge → per-edge-type overrides → edge interaction.
 */
interface StylesheetRule {
  selector: string;
  style: Record<string, unknown>;
}

export function buildAtlasStylesheet(palette: AtlasPalette): cytoscape.StylesheetCSS[] {
  const rules: StylesheetRule[] = [
    {
      selector: 'node',
      style: {
        'background-color': palette.nodeFill,
        'border-color': palette.text1,
        'border-width': 1,
        label: 'data(label)',
        color: palette.text1,
        'text-valign': 'center',
        'text-halign': 'center',
        'font-family': '"IBM Plex Sans", system-ui, sans-serif',
        'font-size': 11,
        'text-max-width': 100,
        'text-wrap': 'ellipsis',
        width: 110,
        height: 40,
        shape: 'rectangle',
      },
    },
    // Family overrides — shape + height per the six-family spec.
    { selector: 'node[family = "workload"]', style: { shape: 'rectangle' } },
    {
      selector: 'node[family = "configuration"]',
      style: { shape: 'round-rectangle', 'corner-radius': '2' },
    },
    {
      selector: 'node[family = "identity"]',
      style: { shape: 'round-rectangle', 'corner-radius': '16', height: 32 },
    },
    { selector: 'node[family = "network"]', style: { shape: 'hexagon', height: 48 } },
    { selector: 'node[family = "storage"]', style: { shape: 'cut-rectangle' } },
    { selector: 'node[family = "custom"]', style: { shape: 'octagon', height: 42 } },
    // State overrides — fill + border per the six base states. Hex
    // colours are mixed manually because Cytoscape can neither read
    // CSS variables nor call color-mix().
    {
      selector: 'node[state = "warning"]',
      style: {
        'background-color': mix(palette.warning, palette.bg, 0.12),
        'border-color': palette.warning,
      },
    },
    {
      selector: 'node[state = "error"]',
      style: {
        'background-color': mix(palette.error, palette.bg, 0.14),
        'border-color': palette.error,
        'border-width': 1.5,
        color: palette.error,
      },
    },
    {
      selector: 'node[state = "orphan"]',
      style: {
        'background-color': mix(palette.orphan, palette.bg, 0.12),
        'border-color': palette.orphan,
        'border-style': 'dashed',
      },
    },
    {
      selector: 'node[state = "deleted"]',
      style: {
        'background-opacity': 0,
        'border-color': palette.text3,
        'border-style': 'dashed',
        color: palette.text3,
      },
    },
    {
      selector: 'node[state = "unknown"]',
      style: {
        'background-color': palette.surface,
        'border-color': palette.text3,
        color: palette.text3,
      },
    },
    // Interaction overrides — selected / dimmed / highlighted.
    {
      selector: 'node:selected',
      style: {
        'border-color': palette.select,
        'border-width': 2,
        'background-color': mix(palette.select, palette.nodeFill, 0.06),
      },
    },
    { selector: 'node[?dimmed]', style: { opacity: 0.2 } },
    { selector: 'node[?highlighted]', style: { opacity: 1, 'z-index': 10 } },
    // ⌘K palette match — thick select-coloured outline so matches
    // stay legible after the overlay closes (search-folds-into-graph).
    {
      selector: 'node[?match]',
      style: {
        'overlay-color': palette.select,
        'overlay-opacity': 0.18,
        'overlay-padding': 6,
        'border-color': palette.select,
        'border-width': 2,
      },
    },
    // Time-axis diff overlays — added (healthy green halo),
    // removed (dashed muted outline), modified (warning amber).
    // Per the design: green halo for added, ochre pulse for
    // modified, dashed outline for deleted. No animation here —
    // prefers-reduced-motion applies and a thick outline reads as
    // well as a pulse on a still canvas.
    {
      selector: 'node[?added]',
      style: {
        'border-color': palette.healthy,
        'border-width': 2.5,
        'overlay-color': palette.healthy,
        'overlay-opacity': 0.10,
        'overlay-padding': 4,
      },
    },
    {
      selector: 'node[?removed]',
      style: {
        'border-color': palette.text3,
        'border-width': 1.5,
        'border-style': 'dashed',
        opacity: 0.55,
      },
    },
    {
      selector: 'node[?modified]',
      style: {
        'border-color': palette.warning,
        'border-width': 2.5,
        'overlay-color': palette.warning,
        'overlay-opacity': 0.10,
        'overlay-padding': 4,
      },
    },

    // Aggregated nodes (cluster / namespace view) still render in
    // the canonical aggregated shape from the legacy backend.
    {
      selector: 'node[type = "aggregated"]',
      style: { shape: 'round-rectangle', width: 110 },
    },

    // Edges — base + per-type cascades.
    {
      selector: 'edge',
      style: {
        width: WEIGHT_PX.medium,
        'line-color': palette.edges.structural,
        'target-arrow-color': palette.edges.structural,
        'curve-style': 'bezier',
        'arrow-scale': 0.9,
        'font-family': '"IBM Plex Sans", system-ui, sans-serif',
        'font-size': 9,
        color: palette.text2,
      },
    },
  ];

  // Per-edge-type rules generated from the channel matrix.
  for (const [type, spec] of Object.entries(EDGE_STYLES)) {
    const color = palette.edges[spec.domain];
    const dash = DASH_PATTERN[spec.dash];
    rules.push({
      selector: `edge[type = "${type}"]`,
      style: {
        width: WEIGHT_PX[spec.weight],
        'line-color': color,
        'target-arrow-color': color,
        'line-style': spec.dash === 'solid' ? 'solid' : 'dashed',
        ...(dash.length > 0
          ? { 'line-dash-pattern': dash, 'line-dash-offset': 0 }
          : {}),
        'target-arrow-shape': ARROW_CY_SHAPE[spec.arrow],
      },
    });
  }

  // Edge interaction overrides.
  rules.push({
    selector: 'edge:selected',
    style: { 'line-color': palette.select, 'target-arrow-color': palette.select, width: 2.25 },
  });
  rules.push({ selector: 'edge[?dimmed]', style: { opacity: 0.15 } });
  rules.push({ selector: 'edge[?highlighted]', style: { opacity: 1, 'z-index': 10 } });

  // Cytoscape's StylesheetCSS type is a discriminated union TS cannot
  // narrow our plain { selector, style } object literals into; the
  // runtime shape is correct and this is the same `as unknown as`
  // cast the legacy stylesheet array used.
  return rules as unknown as cytoscape.StylesheetCSS[];
}

/**
 * mix linearly blends two hex colours. t=1 returns `a`, t=0 returns
 * `b`. The dependency-free CSS color-mix replacement Cytoscape needs
 * (it can read neither CSS variables nor call color-mix()).
 */
export function mix(a: string, b: string, t: number): string {
  const ar = parseInt(a.slice(1, 3), 16);
  const ag = parseInt(a.slice(3, 5), 16);
  const ab = parseInt(a.slice(5, 7), 16);
  const br = parseInt(b.slice(1, 3), 16);
  const bg = parseInt(b.slice(3, 5), 16);
  const bb = parseInt(b.slice(5, 7), 16);
  const r = Math.round(br + (ar - br) * t);
  const g = Math.round(bg + (ag - bg) * t);
  const bl = Math.round(bb + (ab - bb) * t);
  return `#${[r, g, bl].map((x) => x.toString(16).padStart(2, '0')).join('')}`;
}

// ----- View → cytoscape elements ----------------------------------------

const perfOptions = {
  textureOnViewport: true,
  hideEdgesOnViewport: true,
  hideLabelsOnViewport: true,
  pixelRatio: 1,
  wheelSensitivity: 0.2,
};

/**
 * elementsFromView turns a server-side aggregated View into the
 * shape Cytoscape consumes. Adds the cartography data fields
 * (family, state) the stylesheet selectors key on, while preserving
 * the existing fields legacy callers / tests rely on.
 */
export function elementsFromView(view: View): ElementDefinition[] {
  const elements: ElementDefinition[] = [];
  for (const n of view.nodes ?? []) {
    const kind = n.kind ?? '';
    elements.push({
      group: 'nodes',
      data: {
        id: n.id,
        kind,
        label: nodeLabel(n),
        type: n.type,
        family: kind ? classifyKind(kind) : 'custom',
        inferred: kind ? isInferredKind(kind) : true,
        state: 'healthy',
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
        type: e.type ?? '',
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

// ----- Lifecycle --------------------------------------------------------

/** Look up the active atlas palette by theme name. */
export function paletteFor(name: AtlasThemeName = DEFAULT_THEME): AtlasPalette {
  return themePalettes[name];
}

/**
 * createCytoscape boots a cytoscape instance, applies the
 * cartography stylesheet for the given palette, and runs the dagre
 * layout. Callers own the returned Core and must call .destroy() on
 * unmount.
 */
export function createCytoscape(
  container: HTMLElement,
  view: View,
  palette: AtlasPalette = paletteFor(),
): Core {
  ensureDagre();
  const cy = cytoscape({
    container,
    elements: elementsFromView(view),
    style: buildAtlasStylesheet(palette) as unknown as cytoscape.StylesheetCSS[],
    ...perfOptions,
  });
  cy.layout(layout).run();
  return cy;
}

/**
 * updateCytoscape applies a new view to an existing instance via
 * cy.json — a structural diff much cheaper than destroy + recreate.
 */
export function updateCytoscape(cy: Core, view: View): void {
  cy.json({ elements: elementsFromView(view) });
  cy.layout(layout).run();
}

/**
 * applyAtlasPalette rebuilds the stylesheet for a different palette
 * and applies it live. Use it from a theme-change effect so the
 * graph reskins without dropping selection state.
 */
export function applyAtlasPalette(cy: Core, palette: AtlasPalette): void {
  cy
    .style()
    .fromJson(buildAtlasStylesheet(palette) as unknown as cytoscape.StylesheetCSS[])
    .update();
}

// Re-exported for test assertions without reaching into module scope.
export const dagreLayout = layout;

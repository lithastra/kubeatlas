/* ============================================================
 * themePalettes.ts — typed palette bridge.
 *
 * Port of design/starter/themePalettes.ts. Cytoscape cannot read
 * CSS variables; this is the bridge for the future graph stylesheet
 * AND the MUI theme builder (theme.ts). The source-of-truth lives
 * in the lithastra/kubeatlas-design tree as themes.tokens.json — to
 * pick up palette changes, re-derive this file from there. See
 * web/src/theme/README.md.
 * ============================================================ */

export type AtlasScheme = 'light' | 'dark';

export interface AtlasEdgePalette {
  structural: string;
  config: string;
  secret: string;
  identity: string;
  traffic: string;
  policy: string;
  storage: string;
  federation: string;
}

export interface AtlasPalette {
  label: string;
  scheme: AtlasScheme;
  bg: string;
  surface: string;
  border: string;
  text1: string;
  text2: string;
  text3: string;
  nodeFill: string;
  healthy: string;
  warning: string;
  error: string;
  orphan: string;
  select: string;
  edges: AtlasEdgePalette;
}

export const themePalettes = {
  parchment: {
    label: 'Parchment',
    scheme: 'light',
    bg: '#F4EFE6',
    surface: '#ECE3D3',
    border: '#DDD0BB',
    text3: '#B5A48A',
    text2: '#6B5A45',
    text1: '#2B2418',
    nodeFill: '#F4EFE6',
    healthy: '#5C7F6B',
    warning: '#B8893A',
    error: '#A14638',
    orphan: '#7E6BA8',
    select: '#2F5E8C',
    edges: {
      structural: '#5C5142',
      config: '#6B8C76',
      secret: '#9B5B4E',
      identity: '#8A78B3',
      traffic: '#C49441',
      policy: '#4B6E94',
      storage: '#7A6B5A',
      federation: '#A14638',
    },
  },
  survey: {
    label: 'Survey',
    scheme: 'light',
    bg: '#EDF0F3',
    surface: '#E0E6EC',
    border: '#C9D2DB',
    text3: '#8A98A6',
    text2: '#51606E',
    text1: '#1E2A35',
    nodeFill: '#EDF0F3',
    healthy: '#3F7D6E',
    warning: '#A8742B',
    error: '#A8443B',
    orphan: '#665CA0',
    select: '#1F6FA8',
    edges: {
      structural: '#4A5563',
      config: '#3F7D6E',
      secret: '#A8554A',
      identity: '#6E63A8',
      traffic: '#A8742B',
      policy: '#1F6FA8',
      storage: '#5E6B78',
      federation: '#A8443B',
    },
  },
  terrain: {
    label: 'Terrain',
    scheme: 'light',
    bg: '#F1F0E4',
    surface: '#E6E5D2',
    border: '#D2D2BC',
    text3: '#A2A488',
    text2: '#5A5D43',
    text1: '#2C3019',
    nodeFill: '#F1F0E4',
    healthy: '#5A7D4F',
    warning: '#C08A2E',
    error: '#A8503A',
    orphan: '#7B6BA0',
    select: '#386B5E',
    edges: {
      structural: '#5C5238',
      config: '#5A7D4F',
      secret: '#A8503A',
      identity: '#7B6BA0',
      traffic: '#C08A2E',
      policy: '#386B5E',
      storage: '#6E6446',
      federation: '#9E4030',
    },
  },
  ink: {
    label: 'Ink',
    scheme: 'light',
    bg: '#FAFAF8',
    surface: '#F0F0EC',
    border: '#D8D8D2',
    text3: '#9A9A92',
    text2: '#565650',
    text1: '#1A1A17',
    nodeFill: '#FAFAF8',
    healthy: '#4F7060',
    warning: '#9C7128',
    error: '#983A2E',
    orphan: '#6A5E8C',
    select: '#1C5A86',
    edges: {
      structural: '#3A3A35',
      config: '#4F7060',
      secret: '#983A2E',
      identity: '#6A5E8C',
      traffic: '#8A6A2A',
      policy: '#1C5A86',
      storage: '#56564E',
      federation: '#8E3826',
    },
  },
  slate: {
    label: 'Slate',
    scheme: 'dark',
    bg: '#1B1D22',
    surface: '#24272E',
    border: '#353941',
    // text3 bumped from #6B7079 in a v1.3.x a11y pass — the
    // previous value failed WCAG AA normal-text contrast on both
    // bg (3.39:1) and surface (3.00:1). #888E98 gives 5.11:1 on
    // bg and 4.53:1 on surface, both clearing 4.5:1 with margin.
    text3: '#888E98',
    text2: '#A0A6B0',
    text1: '#E8E6DF',
    nodeFill: '#24272E',
    healthy: '#6FA88A',
    warning: '#D3A55C',
    error: '#C46857',
    orphan: '#9A87C6',
    select: '#5B92C9',
    edges: {
      structural: '#9A8E78',
      config: '#7FAE8C',
      secret: '#C4796B',
      identity: '#A593CE',
      traffic: '#D3A55C',
      policy: '#6699C9',
      storage: '#9A8B78',
      federation: '#C46857',
    },
  },
} satisfies Record<string, AtlasPalette>;

export type AtlasThemeName = keyof typeof themePalettes;

export const ATLAS_THEME_NAMES = Object.keys(themePalettes) as AtlasThemeName[];

export const DEFAULT_THEME: AtlasThemeName = 'parchment';
export const DARK_THEME: AtlasThemeName = 'slate';

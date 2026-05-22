/* ============================================================
 * theme.ts — MUI v5 theme builder for KubeAtlas's cartography
 * design system.
 *
 * Why concrete values rather than var(--…) strings: MUI internally
 * runs alpha() / lighten() / darken() on palette colours for hover,
 * ripple, and disabled states, and those helpers can't resolve CSS
 * variables. So MUI gets real hex from themePalettes; our own
 * (non-MUI) components consume var(--atlas-*) from atlas-themes.css.
 * Both sides derive from the same source-of-truth.
 * ============================================================ */
import { createTheme, type Theme } from '@mui/material/styles';

import {
  themePalettes,
  type AtlasPalette,
  type AtlasThemeName,
} from './themePalettes';

function buildTheme(p: AtlasPalette): Theme {
  return createTheme({
    palette: {
      mode: p.scheme,
      background: { default: p.bg, paper: p.surface },
      text: { primary: p.text1, secondary: p.text2, disabled: p.text3 },
      primary: { main: p.select, contrastText: p.bg },
      success: { main: p.healthy },
      warning: { main: p.warning },
      error: { main: p.error },
      divider: p.border,
    },
    typography: {
      fontFamily: '"IBM Plex Sans", system-ui, sans-serif',
      // Inria Serif ships 300/400/700 only — no 500. Headings use
      // 400 (Regular reads cartographic at heading size); 700 is
      // reserved for rare emphasis.
      h1: { fontFamily: '"Inria Serif", serif', fontWeight: 400, fontSize: 32, lineHeight: '38px' },
      h2: { fontFamily: '"Inria Serif", serif', fontWeight: 400, fontSize: 24, lineHeight: '30px' },
      h3: { fontFamily: '"Inria Serif", serif', fontWeight: 400, fontSize: 18, lineHeight: '24px' },
      body1: { fontSize: 14, lineHeight: '20px' },
      body2: { fontSize: 13, lineHeight: '18px' },
      caption: { fontSize: 12, lineHeight: '16px' },
      button: { fontWeight: 500, fontSize: 13 },
    },
    shape: { borderRadius: 2 },
    components: {
      MuiButton: {
        styleOverrides: {
          root: { textTransform: 'none', fontWeight: 500, borderRadius: 2 },
          contained: { boxShadow: 'none', '&:hover': { boxShadow: 'none' } },
        },
      },
      MuiCard: {
        styleOverrides: {
          root: { borderRadius: 0, border: `1px solid ${p.border}`, boxShadow: 'none' },
        },
      },
      MuiPaper: {
        // Kill MUI's default elevation-overlay gradient (especially
        // visible in dark mode).
        styleOverrides: { root: { backgroundImage: 'none' } },
      },
      MuiTooltip: {
        styleOverrides: {
          tooltip: {
            backgroundColor: p.text1,
            color: p.bg,
            fontSize: 12,
            fontFamily: '"IBM Plex Sans", system-ui, sans-serif',
            borderRadius: 2,
            padding: '6px 10px',
          },
          arrow: { color: p.text1 },
        },
      },
      MuiCssBaseline: {
        styleOverrides: {
          // Visible focus ring on everything keyboard-focused.
          '*:focus-visible': {
            outline: `2px solid ${p.select}`,
            outlineOffset: '2px',
          },
        },
      },
    },
  });
}

/** All five themes, pre-built. Swap the active one into ThemeProvider. */
export const atlasThemes: Record<AtlasThemeName, Theme> = Object.fromEntries(
  (Object.keys(themePalettes) as AtlasThemeName[]).map((name) => [
    name,
    buildTheme(themePalettes[name]),
  ]),
) as Record<AtlasThemeName, Theme>;

/** Fetch the pre-built MUI theme for a given atlas theme name. */
export function getAtlasTheme(name: AtlasThemeName): Theme {
  return atlasThemes[name];
}

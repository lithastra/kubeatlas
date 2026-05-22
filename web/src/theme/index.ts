/* ============================================================
 * theme/index.ts — public surface of the cartography theme system.
 *
 * The four moving parts:
 *   - atlas-themes.css     CSS custom properties for all 5 themes
 *                          (token names identical; values differ).
 *   - themePalettes.ts     Typed palette data (Cytoscape can't read
 *                          CSS vars; this is the bridge).
 *   - theme.ts             MUI v5 theme builder, one per atlas theme.
 *   - themeController.ts   Runtime data-theme attribute + persistence.
 *
 * See web/src/theme/README.md for source-of-truth + regeneration.
 * ============================================================ */
import './atlas-themes.css';

export {
  atlasThemes,
  getAtlasTheme,
} from './theme';
export {
  themePalettes,
  ATLAS_THEME_NAMES,
  DEFAULT_THEME,
  DARK_THEME,
  type AtlasPalette,
  type AtlasEdgePalette,
  type AtlasScheme,
  type AtlasThemeName,
} from './themePalettes';
export {
  applyTheme,
  getInitialTheme,
  initTheme,
  setTheme,
} from './themeController';
export { AtlasThemeProvider, useAtlasTheme } from './AtlasThemeProvider';

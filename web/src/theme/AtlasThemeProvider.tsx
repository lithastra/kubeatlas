/* ============================================================
 * AtlasThemeProvider — React root for the cartography theme system.
 *
 * Holds the active theme name in state, swaps both halves on change:
 *
 *   - DOM side: writes `data-theme` on <html> via themeController so
 *     atlas-themes.css re-skins everything that consumes
 *     var(--atlas-*).
 *   - MUI side: rebuilds the MUI Theme via getAtlasTheme(name) and
 *     feeds <ThemeProvider> + <CssBaseline> with it.
 *
 * Descendants read `useAtlasTheme()` to render a switcher; the
 * provider exposes `name` and `setName`.
 * ============================================================ */
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';
import { CssBaseline, ThemeProvider } from '@mui/material';

import { getAtlasTheme } from './theme';
import {
  initTheme,
  setTheme as persistTheme,
} from './themeController';
import { type AtlasThemeName } from './themePalettes';
import { RightPanelProvider } from '../shell/RightPanelContext';
import { SearchProvider } from '../shell/SearchContext';
import { BlastRadiusProvider } from '../shell/BlastRadiusContext';
import { DiffModeProvider } from '../shell/DiffModeContext';
import { ClusterSelectionProvider } from '../shell/ClusterSelectionContext';
import { AnnouncerProvider } from '../shell/AnnouncerContext';

interface AtlasThemeContextValue {
  name: AtlasThemeName;
  setName: (next: AtlasThemeName) => void;
}

const AtlasThemeContext = createContext<AtlasThemeContextValue | null>(null);

export function useAtlasTheme(): AtlasThemeContextValue {
  const ctx = useContext(AtlasThemeContext);
  if (!ctx) {
    throw new Error('useAtlasTheme must be used inside <AtlasThemeProvider>');
  }
  return ctx;
}

interface AtlasThemeProviderProps {
  /**
   * In Headlamp plugin mode the host's resolved colour scheme is
   * passed here so first-load follows it (dark → Slate, else
   * Parchment). Standalone shells leave it undefined and let
   * prefers-color-scheme decide.
   */
  hostScheme?: 'light' | 'dark';
  children: ReactNode;
}

export function AtlasThemeProvider({ hostScheme, children }: AtlasThemeProviderProps) {
  // Resolve the initial theme synchronously, before the first paint —
  // initTheme writes data-theme on <html> as a side effect so
  // var(--atlas-*) consumers don't flash the no-attribute defaults.
  const [name, setNameState] = useState<AtlasThemeName>(() => initTheme(hostScheme));

  const muiTheme = useMemo(() => getAtlasTheme(name), [name]);

  const setName = useCallback((next: AtlasThemeName) => {
    setNameState(next);
    persistTheme(next);
  }, []);

  const value = useMemo(() => ({ name, setName }), [name, setName]);

  return (
    <AtlasThemeContext.Provider value={value}>
      <ThemeProvider theme={muiTheme}>
        <CssBaseline />
        <AnnouncerProvider>
          <RightPanelProvider>
            <SearchProvider>
              <BlastRadiusProvider>
                <DiffModeProvider>
                  <ClusterSelectionProvider>{children}</ClusterSelectionProvider>
                </DiffModeProvider>
              </BlastRadiusProvider>
            </SearchProvider>
          </RightPanelProvider>
        </AnnouncerProvider>
      </ThemeProvider>
    </AtlasThemeContext.Provider>
  );
}

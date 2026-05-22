/* ============================================================
 * themeController.ts — runtime theme switching for KubeAtlas.
 *
 * Sets `data-theme` on <html>, which drives atlas-themes.css
 * (our own components). Pair this with swapping the <ThemeProvider>
 * theme using getAtlasTheme(name) from theme.ts (the MUI side) so
 * one call site updates both halves.
 * ============================================================ */
import {
  ATLAS_THEME_NAMES,
  DARK_THEME,
  DEFAULT_THEME,
  type AtlasThemeName,
} from './themePalettes';

const STORAGE_KEY = 'atlas-theme';

function isAtlasTheme(x: string | null): x is AtlasThemeName {
  return x != null && (ATLAS_THEME_NAMES as string[]).includes(x);
}

/**
 * First-load theme. A previously chosen theme wins; otherwise follow
 * the OS color-scheme preference (dark → Slate, else Parchment).
 * Plugin mode (Headlamp): pass the host's scheme via `hostScheme`
 * to follow it.
 */
export function getInitialTheme(hostScheme?: 'light' | 'dark'): AtlasThemeName {
  if (typeof window === 'undefined') return DEFAULT_THEME; // SSR guard
  const stored = window.localStorage?.getItem(STORAGE_KEY) ?? null;
  if (isAtlasTheme(stored)) return stored;
  if (hostScheme) return hostScheme === 'dark' ? DARK_THEME : DEFAULT_THEME;
  const prefersDark = window.matchMedia?.('(prefers-color-scheme: dark)').matches;
  return prefersDark ? DARK_THEME : DEFAULT_THEME;
}

/** Apply a theme to the DOM (CSS-variable side). Does not persist. */
export function applyTheme(name: AtlasThemeName): void {
  if (typeof document === 'undefined') return;
  document.documentElement.setAttribute('data-theme', name);
}

/** Apply + persist the user's choice. Call from the theme switcher. */
export function setTheme(name: AtlasThemeName): void {
  applyTheme(name);
  try {
    window.localStorage?.setItem(STORAGE_KEY, name);
  } catch {
    /* storage may be unavailable (private mode); theme still applies for the session */
  }
}

/**
 * Call once at startup, before first paint, to avoid a flash of the
 * wrong theme. Returns the resolved name so React state can be seeded
 * with it.
 */
export function initTheme(hostScheme?: 'light' | 'dark'): AtlasThemeName {
  const name = getInitialTheme(hostScheme);
  applyTheme(name);
  return name;
}

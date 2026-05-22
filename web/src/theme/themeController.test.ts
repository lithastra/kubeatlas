/**
 * @jest-environment jsdom
 */
import { applyTheme, getInitialTheme, initTheme, setTheme } from './themeController';

afterEach(() => {
  document.documentElement.removeAttribute('data-theme');
  window.localStorage.clear();
});

describe('themeController', () => {
  it('applyTheme writes data-theme on <html>', () => {
    applyTheme('slate');
    expect(document.documentElement.getAttribute('data-theme')).toBe('slate');
  });

  it('setTheme applies AND persists', () => {
    setTheme('terrain');
    expect(document.documentElement.getAttribute('data-theme')).toBe('terrain');
    expect(window.localStorage.getItem('atlas-theme')).toBe('terrain');
  });

  it('getInitialTheme returns the stored choice when present', () => {
    window.localStorage.setItem('atlas-theme', 'survey');
    expect(getInitialTheme()).toBe('survey');
  });

  it('getInitialTheme rejects a stored garbage value and falls back', () => {
    window.localStorage.setItem('atlas-theme', 'not-a-real-theme');
    // No host hint, default fallback path: prefers-color-scheme isn't
    // matched in jsdom by default → parchment.
    expect(getInitialTheme()).toBe('parchment');
  });

  it('getInitialTheme follows hostScheme when nothing is stored', () => {
    expect(getInitialTheme('dark')).toBe('slate');
    expect(getInitialTheme('light')).toBe('parchment');
  });

  it('initTheme returns a name AND sets the attribute', () => {
    const name = initTheme('dark');
    expect(name).toBe('slate');
    expect(document.documentElement.getAttribute('data-theme')).toBe('slate');
  });
});

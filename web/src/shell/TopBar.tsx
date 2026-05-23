/* ============================================================
 * TopBar — 48px cartography header.
 *
 * Hosts: app wordmark (Inria Serif, mono build tag), the small
 * route nav (kept lean while we transition from page-per-route to
 * mode-per-graph), and the theme switcher. Standalone shell only —
 * Headlamp embeds inside the host's chrome and skips this bar.
 * ============================================================ */
import { Box, Stack, Typography } from '@mui/material';
import { NavLink } from 'react-router-dom';
import { useTranslation } from 'react-i18next';

import { ThemeSwitcher } from './ThemeSwitcher';
import { useSearchOverlay } from './SearchContext';

// Nav items come in three flavours so the bar can dispatch each
// click to the right effect:
//   - 'route'    → react-router NavLink (internal page).
//   - 'action'   → button that calls back into shell context
//                   (Search opens the ⌘K palette, no separate page).
//   - 'external' → <a target="_blank"> to a stable docs URL so the
//                   docs site (Docusaurus) loads instead of an
//                   in-app placeholder page.
type NavEntry =
  | { kind: 'route'; to: string; labelKey: string }
  | { kind: 'action'; id: 'search'; labelKey: string }
  | { kind: 'external'; href: string; labelKey: string };

const DOCS_URL = 'https://docs.kubeatlas.lithastra.com/';

const ROUTES: NavEntry[] = [
  { kind: 'route', to: '/topology', labelKey: 'nav.topology' },
  { kind: 'route', to: '/resources', labelKey: 'nav.resources' },
  { kind: 'route', to: '/snapshots', labelKey: 'nav.snapshots' },
  { kind: 'action', id: 'search', labelKey: 'nav.search' },
  { kind: 'external', href: DOCS_URL, labelKey: 'nav.docs' },
];

interface TopBarProps {
  version?: string;
}

export function TopBar({ version = 'dev' }: TopBarProps) {
  const { t } = useTranslation('translation');
  const { t: tApp } = useTranslation('app');
  const search = useSearchOverlay();
  return (
    <Box
      component="header"
      role="banner"
      sx={{
        height: 'var(--atlas-chrome-top-bar)',
        flexShrink: 0,
        display: 'flex',
        alignItems: 'center',
        paddingInline: 'var(--atlas-space-4)',
        backgroundColor: 'var(--atlas-bg)',
        borderBottom: '1px solid var(--atlas-border)',
      }}
    >
      {/* Skip-to-content link — visually hidden until keyboard-focused,
          then becomes a visible chip in the top-left so keyboard users
          can bypass the chrome and land on the main canvas region. */}
      <Box
        component="a"
        href="#atlas-main"
        sx={{
          position: 'absolute',
          left: 'var(--atlas-space-2)',
          top: 'var(--atlas-space-2)',
          padding: '4px 10px',
          backgroundColor: 'var(--atlas-select)',
          color: 'var(--atlas-bg)',
          fontFamily: 'var(--atlas-font-ui)',
          fontSize: 'var(--atlas-text-caption-size)',
          textDecoration: 'none',
          borderRadius: 'var(--atlas-radius-1)',
          transform: 'translateY(-150%)',
          transition: 'transform var(--atlas-transition-quick)',
          '&:focus-visible': { transform: 'translateY(0)' },
        }}
      >
        Skip to graph
      </Box>
      <Typography
        component="div"
        sx={{
          fontFamily: 'var(--atlas-text-heading-font)',
          fontSize: 'var(--atlas-text-heading-size)',
          lineHeight: 1,
          color: 'var(--atlas-text-1)',
          mr: 'var(--atlas-space-3)',
        }}
      >
        {tApp('name')}
      </Typography>
      <Typography
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 'var(--atlas-text-caption-size)',
          color: 'var(--atlas-text-3)',
        }}
      >
        {tApp('version', { build: version })}
      </Typography>
      <Box sx={{ flexGrow: 1 }} />
      <Stack direction="row" spacing={2} component="nav" aria-label="Primary">
        {ROUTES.map((entry) => {
          const label = t(entry.labelKey);
          if (entry.kind === 'route') {
            return (
              <NavLink
                key={entry.to}
                to={entry.to}
                style={({ isActive }) => navItemStyle(isActive)}
              >
                {label}
              </NavLink>
            );
          }
          if (entry.kind === 'action') {
            return (
              <Box
                key={entry.id}
                component="button"
                type="button"
                onClick={() => search.setOpen(true)}
                aria-label={`${label} (⌘K)`}
                sx={{
                  ...navItemStyle(false),
                  background: 'transparent',
                  border: 'none',
                  borderBottom: '2px solid transparent',
                  cursor: 'pointer',
                  padding: 0,
                  paddingBottom: 2,
                  '&:hover': { color: 'var(--atlas-text-1)' },
                }}
              >
                {label}
              </Box>
            );
          }
          // external
          return (
            <Box
              key={entry.href}
              component="a"
              href={entry.href}
              target="_blank"
              rel="noopener noreferrer"
              sx={navItemStyle(false)}
            >
              {label}
            </Box>
          );
        })}
      </Stack>
      <Box sx={{ ml: 'var(--atlas-space-3)' }}>
        <ThemeSwitcher />
      </Box>
    </Box>
  );
}

// Shared visual treatment so route, action, and external nav items
// read as a single nav row instead of three different elements.
function navItemStyle(isActive: boolean) {
  return {
    fontFamily: 'var(--atlas-font-ui)',
    fontSize: 'var(--atlas-text-caption-size)',
    color: isActive ? 'var(--atlas-text-1)' : 'var(--atlas-text-2)',
    textDecoration: 'none',
    borderBottom: isActive
      ? '2px solid var(--atlas-select)'
      : '2px solid transparent',
    paddingBottom: 2,
  } as const;
}

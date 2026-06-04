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

// Nav items come in two flavours:
//   - 'route'    → react-router NavLink (internal page).
//   - 'external' → <a target="_blank"> to a stable docs URL so the
//                   docs site (Docusaurus) loads instead of an
//                   in-app placeholder page.
//
// Search used to be a third 'action' kind that opened the ⌘K
// palette, but it migrated to the Resources page where it's a
// chip beside the title — closer to where operators are looking
// when they want to find a resource.
type NavEntry =
  | { kind: 'route'; to: string; labelKey: string }
  | { kind: 'external'; href: string; labelKey: string };

const DOCS_URL = 'https://docs.kubeatlas.lithastra.com/';

const ROUTES: NavEntry[] = [
  { kind: 'route', to: '/topology', labelKey: 'nav.topology' },
  { kind: 'route', to: '/resources', labelKey: 'nav.resources' },
  { kind: 'route', to: '/policy', labelKey: 'nav.policy' },
  { kind: 'route', to: '/snapshots', labelKey: 'nav.snapshots' },
  { kind: 'external', href: DOCS_URL, labelKey: 'nav.docs' },
];

interface TopBarProps {
  version?: string;
}

export function TopBar({ version = 'dev' }: TopBarProps) {
  const { t } = useTranslation('translation');
  const { t: tApp } = useTranslation('app');
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
      <Stack direction="row" spacing={2} alignItems="center" component="nav" aria-label="Primary">
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

// Shared visual treatment so route and external nav items read as
// one nav row. Each item is an inline-flex centered box so the
// label sits horizontally and vertically centered inside the click
// target, and the active underline sits flush against the bottom
// edge of the box rather than the baseline of an inline element.
function navItemStyle(isActive: boolean) {
  return {
    display: 'inline-flex',
    alignItems: 'center',
    justifyContent: 'center',
    height: 32,
    paddingInline: 4,
    fontFamily: 'var(--atlas-font-ui)',
    fontSize: 'var(--atlas-text-caption-size)',
    color: isActive ? 'var(--atlas-text-1)' : 'var(--atlas-text-2)',
    textDecoration: 'none',
    borderBottom: isActive
      ? '2px solid var(--atlas-select)'
      : '2px solid transparent',
    lineHeight: 1,
  } as const;
}

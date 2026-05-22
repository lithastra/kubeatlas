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

const ROUTES = [
  { to: '/topology', labelKey: 'nav.topology' },
  { to: '/resources', labelKey: 'nav.resources' },
  { to: '/snapshots', labelKey: 'nav.snapshots' },
  { to: '/search', labelKey: 'nav.search' },
  { to: '/docs', labelKey: 'nav.docs' },
] as const;

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
        {ROUTES.map((r) => (
          <NavLink
            key={r.to}
            to={r.to}
            style={({ isActive }) => ({
              fontFamily: 'var(--atlas-font-ui)',
              fontSize: 'var(--atlas-text-caption-size)',
              color: isActive ? 'var(--atlas-text-1)' : 'var(--atlas-text-2)',
              textDecoration: 'none',
              borderBottom: isActive ? '2px solid var(--atlas-select)' : '2px solid transparent',
              paddingBottom: 2,
            })}
          >
            {t(r.labelKey)}
          </NavLink>
        ))}
      </Stack>
      <Box sx={{ ml: 'var(--atlas-space-3)' }}>
        <ThemeSwitcher />
      </Box>
    </Box>
  );
}

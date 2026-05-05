import { type ReactNode } from 'react';
import {
  AppBar,
  Box,
  Drawer,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Toolbar,
  Typography,
} from '@mui/material';
import AccountTreeIcon from '@mui/icons-material/AccountTree';
import HubIcon from '@mui/icons-material/Hub';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import SearchIcon from '@mui/icons-material/Search';
import { NavLink, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';

const drawerWidth = 220;

// Single source of truth for the four nav items in v0.1.0. Adding a
// page means appending here AND adding a Route in App.tsx. The
// `labelKey` references a key under translation:nav.
const navItems = [
  { to: '/resources', labelKey: 'nav.resources', icon: <AccountTreeIcon /> },
  { to: '/topology', labelKey: 'nav.topology', icon: <HubIcon /> },
  { to: '/search', labelKey: 'nav.search', icon: <SearchIcon /> },
  { to: '/docs', labelKey: 'nav.docs', icon: <MenuBookIcon /> },
];

interface AppShellProps {
  children: ReactNode;
  // Version string shown in the top bar. Wired from a build-time
  // constant in P1-T24 (goreleaser ldflags); for now hard-coded.
  version?: string;
}

// AppShell is the persistent top-bar + left-drawer chrome the whole
// app renders into. Routes nest as children. Visual choices follow
// Headlamp so the Phase 2 plugin port stays cheap.
export function AppShell({ children, version = 'dev' }: AppShellProps) {
  const location = useLocation();
  const { t } = useTranslation('translation');
  const { t: tApp } = useTranslation('app');
  return (
    <Box sx={{ display: 'flex' }}>
      <AppBar
        position="fixed"
        sx={{ zIndex: (t) => t.zIndex.drawer + 1 }}
      >
        <Toolbar>
          <Typography variant="h6" noWrap component="div" sx={{ fontWeight: 700 }}>
            {tApp('name')}
          </Typography>
          <Typography variant="caption" sx={{ ml: 1.5, opacity: 0.7 }}>
            {tApp('version', { build: version })}
          </Typography>
        </Toolbar>
      </AppBar>
      <Drawer
        variant="permanent"
        sx={{
          width: drawerWidth,
          flexShrink: 0,
          [`& .MuiDrawer-paper`]: { width: drawerWidth, boxSizing: 'border-box' },
        }}
      >
        <Toolbar />
        <List>
          {navItems.map((item) => (
            <ListItem key={item.to} disablePadding>
              <ListItemButton
                component={NavLink}
                to={item.to}
                selected={location.pathname.startsWith(item.to)}
              >
                <ListItemIcon>{item.icon}</ListItemIcon>
                <ListItemText primary={t(item.labelKey)} />
              </ListItemButton>
            </ListItem>
          ))}
        </List>
      </Drawer>
      <Box
        component="main"
        sx={{ flexGrow: 1, p: 3, minHeight: '100vh', backgroundColor: 'background.default' }}
      >
        <Toolbar /> {/* spacer pushes content below the fixed AppBar */}
        {children}
      </Box>
    </Box>
  );
}

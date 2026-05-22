/* ============================================================
 * ThemeSwitcher — 5-swatch theme picker in the top bar.
 *
 * The MUI Menu's accessibility (roving focus, Enter to select,
 * Esc to close) covers most of the keyboard contract; each item
 * carries the swatch colour as a visual mark + the theme label
 * as text, so the choice is never colour-only.
 * ============================================================ */
import { useState } from 'react';
import { Box, IconButton, ListItemIcon, Menu, MenuItem, Typography } from '@mui/material';

import { Icon } from '../design';
import {
  ATLAS_THEME_NAMES,
  themePalettes,
  useAtlasTheme,
  type AtlasThemeName,
} from '../theme';

export function ThemeSwitcher() {
  const { name, setName } = useAtlasTheme();
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const open = Boolean(anchor);
  return (
    <>
      <IconButton
        size="small"
        aria-label="Theme"
        aria-haspopup="menu"
        aria-expanded={open || undefined}
        onClick={(e) => setAnchor(e.currentTarget)}
        sx={{ color: 'var(--atlas-text-1)' }}
      >
        <Icon name="settings" size={18} />
      </IconButton>
      <Menu
        anchorEl={anchor}
        open={open}
        onClose={() => setAnchor(null)}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
        transformOrigin={{ vertical: 'top', horizontal: 'right' }}
        slotProps={{
          paper: {
            sx: {
              backgroundColor: 'var(--atlas-surface)',
              border: '1px solid var(--atlas-border)',
              boxShadow: 'var(--atlas-elevation-2)',
              borderRadius: 'var(--atlas-radius-1)',
              minWidth: 180,
            },
          },
        }}
      >
        {ATLAS_THEME_NAMES.map((n) => (
          <ThemeMenuItem
            key={n}
            atlasName={n}
            active={n === name}
            onPick={() => {
              setName(n);
              setAnchor(null);
            }}
          />
        ))}
      </Menu>
    </>
  );
}

interface ItemProps {
  atlasName: AtlasThemeName;
  active: boolean;
  onPick: () => void;
}

function ThemeMenuItem({ atlasName, active, onPick }: ItemProps) {
  const p = themePalettes[atlasName];
  return (
    <MenuItem onClick={onPick} selected={active} sx={{ fontFamily: 'var(--atlas-font-ui)' }}>
      <ListItemIcon sx={{ minWidth: 32 }}>
        <Swatch bg={p.bg} accent={p.select} />
      </ListItemIcon>
      <Typography variant="body2" sx={{ flexGrow: 1 }}>
        {p.label}
      </Typography>
      {active && (
        <Box component="span" sx={{ ml: 1, color: 'var(--atlas-select)' }}>
          <Icon name="status-healthy" size={10} label="Active theme" />
        </Box>
      )}
    </MenuItem>
  );
}

function Swatch({ bg, accent }: { bg: string; accent: string }) {
  return (
    <Box
      aria-hidden
      sx={{
        width: 18,
        height: 18,
        backgroundColor: bg,
        border: `1px solid ${accent}`,
        borderRadius: 0,
      }}
    />
  );
}

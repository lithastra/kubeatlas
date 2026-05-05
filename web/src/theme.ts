import { createTheme } from '@mui/material/styles';

// MUI theme aligned with the Headlamp visual family. Phase 1 §2.2
// commits the v0.1.0 UI to Headlamp-adjacent design choices because
// a Phase 2 Headlamp plugin should be a port, not a rewrite.
//
// Choices that match Headlamp:
//   - Primary blue around #1976d2 (MUI blue 700; Headlamp's default)
//   - 4px corner radius (MUI default; Headlamp keeps it)
//   - 8px spacing unit (MUI default)
//   - Inter font with the standard system fallback chain
//
// Choices held back for later phases:
//   - Dark mode (mode: 'light' for v0.1.0; toggle UI lives in v1.0+)
//   - Customisable logo / brand color (v0.1.0 ships a fixed text logo)
export const theme = createTheme({
  palette: {
    mode: 'light',
    primary: {
      main: '#1976d2',
    },
    secondary: {
      main: '#7e57c2',
    },
    background: {
      default: '#fafafa',
      paper: '#ffffff',
    },
  },
  shape: {
    borderRadius: 4,
  },
  typography: {
    fontFamily: [
      '"Inter"',
      'system-ui',
      '-apple-system',
      'BlinkMacSystemFont',
      '"Segoe UI"',
      'Roboto',
      '"Helvetica Neue"',
      'Arial',
      'sans-serif',
    ].join(','),
    h1: { fontWeight: 600 },
    h2: { fontWeight: 600 },
    h3: { fontWeight: 600 },
    button: { textTransform: 'none' },
  },
  components: {
    MuiAppBar: {
      defaultProps: { elevation: 1 },
    },
    MuiButton: {
      defaultProps: { disableElevation: true },
    },
  },
});

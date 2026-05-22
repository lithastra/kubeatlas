/* ============================================================
 * LeftClusterStrip — 56px vertical column for multi-cluster mode.
 *
 * Lists the federation members as colour chips when the server is
 * in federated mode; in single-cluster mode it shows nothing (the
 * strip stays as visual ballast so chrome dimensions match the
 * design's mockups across both modes).
 *
 * Wiring to /api/v1/federation/clusters lands in M5 with the
 * cluster switcher; for M4 we render a static placeholder that
 * uses the theme's swatch so theme switching is visibly testable
 * in the strip too.
 * ============================================================ */
import { Box, Stack, Tooltip, Typography } from '@mui/material';

export function LeftClusterStrip() {
  return (
    <Box
      role="region"
      aria-label="Cluster strip"
      sx={{
        width: 'var(--atlas-chrome-left-cluster-strip)',
        flexShrink: 0,
        backgroundColor: 'var(--atlas-surface)',
        borderInlineEnd: '1px solid var(--atlas-border)',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        paddingBlock: 'var(--atlas-space-3)',
      }}
    >
      <Tooltip title="Federation cluster switcher (M5)" placement="right">
        <Stack alignItems="center" spacing={2}>
          <ClusterDot label="LOC" tone="select" />
          <ClusterDot label="—" tone="border" />
        </Stack>
      </Tooltip>
    </Box>
  );
}

function ClusterDot({ label, tone }: { label: string; tone: 'select' | 'border' }) {
  return (
    <Box
      aria-hidden
      sx={{
        width: 32,
        height: 32,
        borderRadius: 0,
        backgroundColor: tone === 'select' ? 'var(--atlas-select)' : 'transparent',
        border: `1px solid var(--atlas-${tone === 'select' ? 'select' : 'border'})`,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
      }}
    >
      <Typography
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: '10px',
          letterSpacing: '0.04em',
          color: tone === 'select' ? 'var(--atlas-bg)' : 'var(--atlas-text-3)',
        }}
      >
        {label}
      </Typography>
    </Box>
  );
}

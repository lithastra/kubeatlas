/* ============================================================
 * StatusPill — six health states with redundant shape encoding.
 *
 * Colour is not the only signal: every variant carries a
 * differently-shaped icon (●/▲/✕/⌀/?/strikethrough) so the pill
 * stays unambiguous in colour-blind simulation and in greyscale.
 * The label text spells the state out so screen readers also
 * convey the information without colour cues.
 * ============================================================ */
import { Box, type SxProps, type Theme } from '@mui/material';

import { Icon, type AtlasIconName } from './Icon';

export type AtlasStatusVariant =
  | 'healthy'
  | 'warning'
  | 'error'
  | 'orphan'
  | 'deleted'
  | 'unknown';

export type AtlasStatusSize = 'sm' | 'md';

interface StatusPillProps {
  variant: AtlasStatusVariant;
  size?: AtlasStatusSize;
  /**
   * Visible label. Defaults to the title-cased variant name; pass a
   * custom string to disambiguate (e.g. "Healthy (3/3 pods)").
   */
  label?: string;
}

// Per design/07-components.md §4. Colours come from CSS variables so
// every theme reskins the pill correctly; the `warning` text uses
// text-1 because warning at 2.8:1 fails AA on small text.
const VARIANT_STYLES: Record<
  AtlasStatusVariant,
  { fill: string; text: string; icon: AtlasIconName }
> = {
  healthy: {
    fill: 'color-mix(in srgb, var(--atlas-healthy) 15%, transparent)',
    text: 'var(--atlas-healthy)',
    icon: 'status-healthy',
  },
  warning: {
    fill: 'color-mix(in srgb, var(--atlas-warning) 15%, transparent)',
    text: 'var(--atlas-text-1)',
    icon: 'status-warning',
  },
  error: {
    fill: 'color-mix(in srgb, var(--atlas-error) 15%, transparent)',
    text: 'var(--atlas-error)',
    icon: 'status-error',
  },
  orphan: {
    fill: 'color-mix(in srgb, var(--atlas-orphan) 12%, transparent)',
    text: 'var(--atlas-text-1)',
    icon: 'status-orphan',
  },
  deleted: {
    fill: 'transparent',
    text: 'var(--atlas-text-3)',
    icon: 'status-deleted',
  },
  unknown: {
    fill: 'var(--atlas-surface)',
    text: 'var(--atlas-text-3)',
    icon: 'status-unknown',
  },
};

const DEFAULT_LABELS: Record<AtlasStatusVariant, string> = {
  healthy: 'Healthy',
  warning: 'Warning',
  error: 'Error',
  orphan: 'Orphan',
  deleted: 'Deleted',
  unknown: 'Unknown',
};

export function StatusPill({ variant, size = 'md', label }: StatusPillProps) {
  const v = VARIANT_STYLES[variant];
  const text = label ?? DEFAULT_LABELS[variant];
  const heightPx = size === 'sm' ? 16 : 20;
  const iconPx = size === 'sm' ? 9 : 11;
  const sx: SxProps<Theme> = {
    display: 'inline-flex',
    alignItems: 'center',
    gap: '6px',
    height: `${heightPx}px`,
    paddingInline: '8px',
    borderRadius: 'var(--atlas-radius-pill)',
    backgroundColor: v.fill,
    color: v.text,
    fontFamily: 'var(--atlas-font-ui)',
    fontSize: 'var(--atlas-text-caption-size)',
    lineHeight: 1,
    whiteSpace: 'nowrap',
    // Deleted variant: visually strike through, keep text legible.
    textDecoration: variant === 'deleted' ? 'line-through' : 'none',
    // Hairline border in unknown/deleted to maintain edge presence.
    border:
      variant === 'unknown' || variant === 'deleted'
        ? '1px solid var(--atlas-border)'
        : 'none',
  };
  return (
    <Box component="span" sx={sx} aria-label={text}>
      <Icon name={v.icon} size={iconPx} />
      <span>{text}</span>
    </Box>
  );
}

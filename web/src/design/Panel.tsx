/* ============================================================
 * Panel — surface container with three variants.
 *
 *   - panel  : chrome container (right detail panel, left strip).
 *   - card   : inset block within a panel (status card, list item).
 *   - inset  : double-inset (code block, list-within-card).
 *
 * Square corners by default (radius-0) — cartography is sharp; the
 * round-corner Card grid is on the "don't" list (CLAUDE.md). Roving
 * tabindex for children is the consumer's job; this only sets the
 * region role + label.
 * ============================================================ */
import { Box, type SxProps, type Theme } from '@mui/material';
import type { ElementType, ReactNode } from 'react';

export type AtlasPanelVariant = 'panel' | 'card' | 'inset';
export type AtlasPanelPadding = 0 | 1 | 2 | 3 | 4 | 6;

interface PanelProps {
  variant?: AtlasPanelVariant;
  /** Space token applied as padding (`--atlas-space-N`). 0 = none. */
  padding?: AtlasPanelPadding;
  /** Render as a <section role="region"> when truthy and pass to aria-label. */
  ariaLabel?: string;
  as?: ElementType;
  className?: string;
  sx?: SxProps<Theme>;
  children?: ReactNode;
}

const VARIANT_SX: Record<AtlasPanelVariant, SxProps<Theme>> = {
  panel: {
    backgroundColor: 'var(--atlas-surface)',
    borderInlineStart: '1px solid var(--atlas-border)',
    borderRadius: 'var(--atlas-radius-0)',
  },
  card: {
    backgroundColor: 'var(--atlas-bg)',
    border: '1px solid var(--atlas-border)',
    borderRadius: 'var(--atlas-radius-0)',
  },
  inset: {
    backgroundColor: 'color-mix(in srgb, var(--atlas-text-1) 4%, var(--atlas-surface))',
    borderRadius: 'var(--atlas-radius-0)',
  },
};

export function Panel({
  variant = 'panel',
  padding = 3,
  ariaLabel,
  as,
  className,
  sx,
  children,
}: PanelProps) {
  const Component: ElementType = as ?? (ariaLabel ? 'section' : 'div');
  const accessibilityProps = ariaLabel
    ? { role: 'region' as const, 'aria-label': ariaLabel }
    : undefined;
  return (
    <Box
      component={Component}
      className={className}
      sx={[
        VARIANT_SX[variant],
        { padding: padding === 0 ? 0 : `var(--atlas-space-${padding})` },
        ...(Array.isArray(sx) ? sx : sx ? [sx] : []),
      ]}
      {...accessibilityProps}
    >
      {children}
    </Box>
  );
}

/* ============================================================
 * AtlasTooltip — wraps MUI's Tooltip with the cartography defaults
 * (text-1 bg / bg text, 2px radius, caption font) and adds a rich
 * variant: a multi-line tooltip with monospace technical strings.
 *
 * MUI's Tooltip already opens on focus by default; we lean on that
 * so keyboard / long-press paths work without extra wiring.
 * ============================================================ */
import { Tooltip, type TooltipProps } from '@mui/material';
import { type ReactNode } from 'react';

import { Panel } from './Panel';

export type AtlasTooltipVariant = 'default' | 'rich';

interface AtlasTooltipProps extends Omit<TooltipProps, 'title' | 'children'> {
  variant?: AtlasTooltipVariant;
  /** Tooltip content. For `rich`, can be ReactNode with mono detail. */
  title: ReactNode;
  /** The element the tooltip describes. */
  children: TooltipProps['children'];
}

export function AtlasTooltip({
  variant = 'default',
  title,
  children,
  ...rest
}: AtlasTooltipProps) {
  const content =
    variant === 'rich' ? (
      <Panel
        variant="inset"
        padding={2}
        sx={{
          maxWidth: 320,
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 'var(--atlas-text-mono-size)',
          lineHeight: 'var(--atlas-text-mono-lh)',
          color: 'var(--atlas-bg)',
          backgroundColor: 'transparent',
        }}
      >
        {title}
      </Panel>
    ) : (
      title
    );
  return (
    <Tooltip title={content} arrow {...rest}>
      {children}
    </Tooltip>
  );
}

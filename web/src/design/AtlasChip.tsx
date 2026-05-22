/* ============================================================
 * AtlasChip — three cartography variants on top of MUI's Chip.
 *
 *   - cluster: a selectable identifier (left cluster strip).
 *   - filter : a toggleable filter (edge-type, namespace).
 *   - tag    : a read-only label (team/role/version).
 *
 * The variants differ structurally — a `filter` chip in its `on`
 * state shows a checkmark mark redundantly with the colour fill so
 * the on/off distinction is not colour-only.
 * ============================================================ */
import { Box, Chip, type ChipProps, type SxProps, type Theme } from '@mui/material';

import { Icon } from './Icon';

export type AtlasChipVariant = 'cluster' | 'filter' | 'tag';

interface AtlasChipProps extends Omit<ChipProps, 'variant' | 'icon'> {
  atlasVariant: AtlasChipVariant;
  /** Selection state. Drives ARIA + visual on/off encoding. */
  selected?: boolean;
}

export function AtlasChip({
  atlasVariant,
  selected = false,
  label,
  onClick,
  ...rest
}: AtlasChipProps) {
  const isClickable = atlasVariant !== 'tag' && onClick != null;
  const sx = chipSx(atlasVariant, selected);
  const ariaProps = ariaForVariant(atlasVariant, selected, isClickable);
  // `filter` carries a check mark when `on` for non-colour redundancy.
  const startMark =
    atlasVariant === 'filter' && selected ? (
      <Box component="span" sx={{ display: 'inline-flex', mr: '4px' }}>
        <Icon name="status-healthy" size={8} />
      </Box>
    ) : null;
  return (
    <Chip
      label={
        startMark ? (
          <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center' }}>
            {startMark}
            {label}
          </Box>
        ) : (
          label
        )
      }
      onClick={isClickable ? onClick : undefined}
      sx={sx}
      {...ariaProps}
      {...rest}
    />
  );
}

function chipSx(variant: AtlasChipVariant, selected: boolean): SxProps<Theme> {
  const base: SxProps<Theme> = {
    fontFamily: 'var(--atlas-font-ui)',
    fontSize: 'var(--atlas-text-caption-size)',
    lineHeight: 1,
    borderRadius: 'var(--atlas-radius-pill)',
    height: 22,
    border: '1px solid var(--atlas-border)',
  };
  switch (variant) {
    case 'cluster':
      return {
        ...base,
        backgroundColor: 'var(--atlas-surface)',
        color: 'var(--atlas-text-1)',
        ...(selected && {
          // Selected: a 2px inner ring (border-style approach so MUI's
          // outline doesn't double up with focus-visible).
          boxShadow: 'inset 0 0 0 2px var(--atlas-select)',
          backgroundColor: 'color-mix(in srgb, var(--atlas-select) 8%, var(--atlas-surface))',
        }),
      };
    case 'filter':
      return {
        ...base,
        backgroundColor: selected
          ? 'color-mix(in srgb, var(--atlas-select) 12%, var(--atlas-bg))'
          : 'transparent',
        color: 'var(--atlas-text-1)',
      };
    case 'tag':
      return {
        ...base,
        backgroundColor: 'var(--atlas-surface)',
        color: 'var(--atlas-text-2)',
        cursor: 'default',
      };
  }
}

function ariaForVariant(
  variant: AtlasChipVariant,
  selected: boolean,
  isClickable: boolean,
): Partial<ChipProps> {
  if (variant === 'tag') return { 'aria-readonly': true };
  if (variant === 'filter')
    return { role: 'switch', 'aria-checked': selected, clickable: isClickable };
  return { role: 'option', 'aria-selected': selected, clickable: isClickable };
}

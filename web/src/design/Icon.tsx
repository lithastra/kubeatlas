/* ============================================================
 * Icon — sprite consumer.
 *
 * Renders one symbol from the local SVG sprite at design/icons.svg.
 * Colour is `currentColor` so callers control it with CSS (or with
 * `color` prop). No external icon library is imported anywhere in
 * KubeAtlas (CLAUDE.md non-negotiable).
 *
 * Add new icons by extending icons.svg with another <symbol id>;
 * the name is the suffix after "atlas-icon-".
 * ============================================================ */
import type { CSSProperties } from 'react';

// Vite resolves `?url` to the post-build asset URL so the sprite is
// served once and consumers reference symbols via <use href="url#id">.
import spriteUrl from './icons.svg?url';

export type AtlasIconName =
  | 'compass'
  | 'search'
  | 'settings'
  | 'close'
  | 'info'
  | 'menu'
  | 'chevron-down'
  | 'chevron-right'
  | 'status-healthy'
  | 'status-warning'
  | 'status-error'
  | 'status-orphan'
  | 'status-unknown'
  | 'status-deleted';

interface IconProps {
  name: AtlasIconName;
  /** Pixel size for both width and height. Defaults to 16. */
  size?: number;
  /** Override colour; defaults to currentColor. */
  color?: string;
  /** A11y label. Omit for purely decorative use; if omitted the
   *  icon is marked `aria-hidden="true"`. */
  label?: string;
  className?: string;
  style?: CSSProperties;
}

export function Icon({ name, size = 16, color, label, className, style }: IconProps) {
  const ariaProps = label
    ? { role: 'img' as const, 'aria-label': label }
    : { 'aria-hidden': true as const, focusable: false };
  return (
    <svg
      width={size}
      height={size}
      className={className}
      style={{ color: color, flexShrink: 0, verticalAlign: '-0.15em', ...style }}
      {...ariaProps}
    >
      <use href={`${spriteUrl}#atlas-icon-${name}`} />
    </svg>
  );
}

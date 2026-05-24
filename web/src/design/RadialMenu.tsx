/* ============================================================
 * RadialMenu — wedge menu primitive.
 *
 * A circular fan of options anchored to a viewport coordinate.
 * Used today as the right-click entry path for blast-radius mode
 * (pick depth + direction in one gesture); reusable for any other
 * "fast, in-place picker over a graph node" surface that lands
 * later.
 *
 * Interaction:
 *   - Click outside the menu       → close
 *   - Esc                          → close
 *   - Tab into the menu            → first option focused
 *   - Arrow Right / Down           → next option
 *   - Arrow Left  / Up             → previous option
 *   - Enter / Space                → activate focused option
 *
 * Geometry:
 *   - position: fixed at anchor.{x,y}, centred via translate(-50% -50%).
 *   - Each option is placed on a ring of `radius` pixels from the
 *     centre, starting at 12 o'clock and going clockwise.
 *
 * Closed state renders nothing.
 * ============================================================ */
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
} from 'react';
import { Box } from '@mui/material';

export interface RadialMenuOption {
  id: string;
  label: string;
  onSelect: () => void;
  active?: boolean;
}

interface RadialMenuProps {
  open: boolean;
  anchor: { x: number; y: number } | null;
  options: RadialMenuOption[];
  onClose: () => void;
  /** Distance from menu centre to each option chip. Default 72. */
  radius?: number;
  /** aria-label for the menu region. */
  label?: string;
}

const DEFAULT_RADIUS = 72;

export function RadialMenu({
  open,
  anchor,
  options,
  onClose,
  radius = DEFAULT_RADIUS,
  label = 'Radial menu',
}: RadialMenuProps) {
  const [focusIdx, setFocusIdx] = useState(0);
  const buttonsRef = useRef<Array<HTMLElement | null>>([]);

  // Reset focus to the active option (if any) every time the menu
  // opens. Falls back to the first option.
  useEffect(() => {
    if (!open) return;
    const activeIdx = options.findIndex((o) => o.active);
    setFocusIdx(activeIdx >= 0 ? activeIdx : 0);
  }, [open, options]);

  // Move browser focus to whichever option is currently focused so
  // screen readers + visible focus rings agree with arrow-key state.
  useEffect(() => {
    if (!open) return;
    const el = buttonsRef.current[focusIdx];
    el?.focus();
  }, [open, focusIdx]);

  const onKeyDown = useCallback(
    (e: KeyboardEvent<HTMLDivElement>) => {
      if (!open) return;
      switch (e.key) {
        case 'Escape':
          e.preventDefault();
          onClose();
          break;
        case 'ArrowRight':
        case 'ArrowDown':
          e.preventDefault();
          setFocusIdx((i) => (i + 1) % options.length);
          break;
        case 'ArrowLeft':
        case 'ArrowUp':
          e.preventDefault();
          setFocusIdx((i) => (i - 1 + options.length) % options.length);
          break;
        case 'Enter':
        case ' ':
          e.preventDefault();
          options[focusIdx]?.onSelect();
          onClose();
          break;
      }
    },
    [open, options, focusIdx, onClose],
  );

  if (!open || !anchor || options.length === 0) return null;

  return (
    <>
      {/* Backdrop — captures clicks outside the menu so they close
          the menu instead of falling through to the canvas. */}
      <Box
        aria-hidden
        onClick={onClose}
        sx={{
          position: 'fixed',
          inset: 0,
          zIndex: 1100,
          backgroundColor: 'transparent',
        }}
      />
      <Box
        role="menu"
        aria-label={label}
        onKeyDown={onKeyDown}
        sx={{
          position: 'fixed',
          left: anchor.x,
          top: anchor.y,
          transform: 'translate(-50%, -50%)',
          width: 2 * radius + 64,
          height: 2 * radius + 64,
          zIndex: 1101,
          pointerEvents: 'none',
        }}
      >
        {/* Centre dot — visual anchor so the operator can see the
            point the radial fans out from. */}
        <Box
          aria-hidden
          sx={{
            position: 'absolute',
            left: '50%',
            top: '50%',
            transform: 'translate(-50%, -50%)',
            width: 8,
            height: 8,
            borderRadius: '50%',
            backgroundColor: 'var(--atlas-text-3)',
            opacity: 0.6,
          }}
        />
        {options.map((opt, i) => {
          // 12 o'clock = -π/2, clockwise from there.
          const angle = (2 * Math.PI * i) / options.length - Math.PI / 2;
          const cx = radius + 32; // chip half-width offset
          const cy = radius + 32;
          const x = cx + radius * Math.cos(angle);
          const y = cy + radius * Math.sin(angle);
          const isFocused = i === focusIdx;
          return (
            <Box
              key={opt.id}
              component="button"
              type="button"
              role="menuitem"
              ref={(el: HTMLButtonElement | null) => {
                buttonsRef.current[i] = el;
              }}
              tabIndex={isFocused ? 0 : -1}
              aria-current={opt.active ? 'true' : undefined}
              onClick={() => {
                opt.onSelect();
                onClose();
              }}
              sx={{
                position: 'absolute',
                left: x,
                top: y,
                transform: 'translate(-50%, -50%)',
                pointerEvents: 'auto',
                minWidth: 40,
                height: 40,
                padding: '0 10px',
                borderRadius: '20px',
                border: '1.5px solid',
                borderColor: opt.active
                  ? 'var(--atlas-select)'
                  : 'var(--atlas-border)',
                background: opt.active
                  ? 'color-mix(in srgb, var(--atlas-select) 18%, var(--atlas-bg))'
                  : 'var(--atlas-bg)',
                color: opt.active ? 'var(--atlas-select)' : 'var(--atlas-text-1)',
                fontFamily: 'var(--atlas-font-mono)',
                fontSize: 12,
                fontWeight: 600,
                cursor: 'pointer',
                boxShadow: '0 1px 3px rgba(0,0,0,0.18)',
                transition: 'border-color 0.12s, transform 0.12s',
                '&:hover': {
                  borderColor: 'var(--atlas-select)',
                  transform: 'translate(-50%, -50%) scale(1.05)',
                },
                '&:focus-visible': {
                  outline: '2px solid var(--atlas-select)',
                  outlineOffset: 2,
                },
              }}
            >
              {opt.label}
            </Box>
          );
        })}
      </Box>
    </>
  );
}

/* ============================================================
 * TimeAxisBar — 32px persistent time scrubber + anchor presets.
 *
 * The cartography time axis is always visible (it's the 4th
 * dimension of the explorer). The playhead lives at the right edge
 * = NOW. Operators set an anchor in the past via one of two paths:
 *
 *   - Click a preset chip (1h / 4h / 24h / 7d ago) — the common
 *     windows, keyboard-reachable.
 *   - Drag on the rail itself — picks an arbitrary instant inside
 *     the last 7 days. The rail is a real ARIA slider with arrow-
 *     key + Home/End support; Shift-arrow steps in 10-minute jumps
 *     instead of 1-minute jumps so wide ranges aren't tedious.
 *
 * The anchor is a duration string the snapshot diff API accepts
 * directly ("1h", "4h", "30m", "2d", …). Anchor=null = no diff
 * mode.
 * ============================================================ */
import { useCallback, useRef, type KeyboardEvent, type PointerEvent } from 'react';
import { Box, Stack, Typography } from '@mui/material';

import { useDiffMode } from './DiffModeContext';

const ANCHOR_PRESETS = [
  { value: '1h', label: '1h ago' },
  { value: '4h', label: '4h ago' },
  { value: '24h', label: '24h ago' },
  { value: '7d', label: '7d ago' },
] as const;

// Rail span: how far back the leftmost edge of the rail represents.
// Seven days matches the longest preset chip and the default Tier 2
// snapshot retention; anything beyond that clamps to the left edge
// so the marker stays visible.
const RAIL_SPAN_SEC = 7 * 86400;

// Minimum step size for keyboard nudging — 1m by default, 10m with
// Shift. Small enough to feel precise; large enough that a Home to
// End sweep doesn't take a hundred thousand key presses.
const KEY_STEP_SEC = 60;
const KEY_STEP_SEC_SHIFT = 600;

export function TimeAxisBar() {
  const { anchor, setAnchor, exit } = useDiffMode();
  const railRef = useRef<HTMLDivElement | null>(null);
  const draggingRef = useRef(false);

  const updateFromPointer = useCallback(
    (clientX: number) => {
      const el = railRef.current;
      if (!el) return;
      const rect = el.getBoundingClientRect();
      if (rect.width <= 0) return;
      const f = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
      setAnchor(fractionToAnchor(f));
    },
    [setAnchor],
  );

  const onRailPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    e.currentTarget.setPointerCapture(e.pointerId);
    draggingRef.current = true;
    updateFromPointer(e.clientX);
  };
  const onRailPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    if (!draggingRef.current) return;
    updateFromPointer(e.clientX);
  };
  const onRailPointerUp = (e: PointerEvent<HTMLDivElement>) => {
    if (draggingRef.current) {
      e.currentTarget.releasePointerCapture(e.pointerId);
      draggingRef.current = false;
    }
  };

  const onRailKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const step = e.shiftKey ? KEY_STEP_SEC_SHIFT : KEY_STEP_SEC;
    const cur = anchor ? (parseDurationSec(anchor) ?? 3600) : 0;
    switch (e.key) {
      case 'ArrowLeft':
        // Marker leftward = further in the past = larger duration.
        e.preventDefault();
        setAnchor(formatDurationSec(Math.min(RAIL_SPAN_SEC, cur + step)));
        break;
      case 'ArrowRight': {
        // Rightward = closer to NOW = smaller duration. Hitting 0
        // clears the anchor (= NOW = no diff mode).
        e.preventDefault();
        const next = Math.max(0, cur - step);
        setAnchor(next === 0 ? null : formatDurationSec(next));
        break;
      }
      case 'Home':
        e.preventDefault();
        setAnchor(formatDurationSec(RAIL_SPAN_SEC));
        break;
      case 'End':
        e.preventDefault();
        setAnchor(null);
        break;
      case 'Escape':
        if (anchor) {
          e.preventDefault();
          exit();
        }
        break;
    }
  };

  const fraction = anchor ? anchorToFraction(anchor) : 1;

  return (
    <Box
      role="region"
      aria-label="Time axis"
      sx={{
        height: 'var(--atlas-chrome-time-axis)',
        flexShrink: 0,
        backgroundColor: 'var(--atlas-bg)',
        borderBottom: '1px solid var(--atlas-border)',
        display: 'flex',
        alignItems: 'center',
        paddingInline: 'var(--atlas-space-4)',
        gap: 'var(--atlas-space-3)',
      }}
    >
      <Typography
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 'var(--atlas-text-caption-size)',
          color: 'var(--atlas-text-3)',
          flexShrink: 0,
        }}
      >
        {anchor ? `anchor: ${anchor} ago` : 'anchor:'}
      </Typography>

      <Stack direction="row" spacing={0.5} sx={{ flexShrink: 0 }}>
        {ANCHOR_PRESETS.map((p) => {
          const isActive = anchor === p.value;
          return (
            <Box
              key={p.value}
              component="button"
              type="button"
              onClick={() => setAnchor(isActive ? null : p.value)}
              aria-pressed={isActive}
              sx={{
                padding: '2px 8px',
                border: '1px solid',
                borderColor: isActive ? 'var(--atlas-select)' : 'var(--atlas-border)',
                background: isActive
                  ? 'color-mix(in srgb, var(--atlas-select) 18%, transparent)'
                  : 'transparent',
                fontFamily: 'var(--atlas-font-mono)',
                fontSize: 11,
                color: isActive ? 'var(--atlas-select)' : 'var(--atlas-text-2)',
                cursor: 'pointer',
                '&:hover': { borderColor: 'var(--atlas-select)' },
                '&:focus-visible': {
                  outline: '2px solid var(--atlas-select)',
                  outlineOffset: 1,
                },
              }}
            >
              {p.label}
            </Box>
          );
        })}
        {anchor && (
          <Box
            component="button"
            type="button"
            onClick={exit}
            sx={{
              padding: '2px 8px',
              border: '1px solid var(--atlas-border)',
              background: 'transparent',
              fontFamily: 'var(--atlas-font-mono)',
              fontSize: 11,
              color: 'var(--atlas-text-3)',
              cursor: 'pointer',
              ml: 1,
              '&:hover': { color: 'var(--atlas-text-1)' },
            }}
          >
            clear
          </Box>
        )}
      </Stack>

      <Box
        ref={railRef}
        role="slider"
        aria-label="Diff anchor — drag or use arrow keys to pick a time within the last 7 days"
        aria-valuemin={0}
        aria-valuemax={1}
        aria-valuenow={Number(fraction.toFixed(3))}
        aria-valuetext={anchor ? `${anchor} ago` : 'now (no anchor)'}
        tabIndex={0}
        onPointerDown={onRailPointerDown}
        onPointerMove={onRailPointerMove}
        onPointerUp={onRailPointerUp}
        onPointerCancel={onRailPointerUp}
        onKeyDown={onRailKeyDown}
        sx={{
          flexGrow: 1,
          height: 16,
          position: 'relative',
          cursor: 'pointer',
          touchAction: 'none',
          outline: 'none',
          '&:focus-visible': {
            boxShadow: 'inset 0 0 0 2px var(--atlas-select)',
            borderRadius: 1,
          },
          // Rail line.
          '&::before': {
            content: '""',
            position: 'absolute',
            left: 0,
            right: 0,
            top: '50%',
            height: 2,
            transform: 'translateY(-50%)',
            background: 'var(--atlas-border)',
          },
          // Anchor marker — purple tick whose left% is the real
          // fraction of the rail span the anchor sits at. When no
          // anchor is set, fraction == 1 so the marker would land
          // on the playhead; hidden via the conditional spread.
          ...(anchor
            ? {
                '&::after': {
                  content: '""',
                  position: 'absolute',
                  top: '50%',
                  left: `${fraction * 100}%`,
                  width: 2,
                  height: 10,
                  transform: 'translate(-50%, -50%)',
                  background: '#7E6BA8',
                },
              }
            : {}),
        }}
      />

      {/* Playhead — always at the right edge (NOW). Lives outside the
          slider so it doesn't get caught by the pointer handlers. */}
      <Box
        aria-hidden
        sx={{
          width: 2,
          height: 10,
          backgroundColor: 'var(--atlas-select)',
          flexShrink: 0,
          marginInlineStart: '-6px',
        }}
      />

      <Typography
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 'var(--atlas-text-caption-size)',
          color: 'var(--atlas-text-3)',
          flexShrink: 0,
        }}
      >
        now
      </Typography>
    </Box>
  );
}

// ----- duration helpers ----------------------------------------------

// Parses a snapshot-diff duration string into seconds. Returns null
// when the string doesn't match the API's supported shape — the
// caller treats null as "unknown, ignore".
function parseDurationSec(s: string): number | null {
  const m = /^(\d+)(s|m|h|d)$/.exec(s);
  if (!m) return null;
  const n = parseInt(m[1], 10);
  const unitSec: Record<string, number> = { s: 1, m: 60, h: 3600, d: 86400 };
  const unit = unitSec[m[2]];
  return unit ? n * unit : null;
}

// Inverse of parseDurationSec — picks the most readable unit for
// the given second count, biased toward whole numbers (rounds
// rather than truncates).
function formatDurationSec(sec: number): string {
  sec = Math.max(0, Math.round(sec));
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.round(sec / 60)}m`;
  if (sec < 86400) return `${Math.round(sec / 3600)}h`;
  return `${Math.round(sec / 86400)}d`;
}

// Map a rail fraction (0 = oldest, 1 = NOW) to an anchor duration
// string, or null when the fraction lands at NOW (the snap zone).
// The 30s snap window at the right edge means a careful click near
// "now" doesn't accidentally set a 1-second anchor.
function fractionToAnchor(f: number): string | null {
  const sec = Math.round((1 - f) * RAIL_SPAN_SEC);
  if (sec < 30) return null;
  return formatDurationSec(sec);
}

// Inverse — where on the rail an anchor sits. Unknown or longer
// than the rail span clamps to the leftmost edge so the marker
// stays visible.
function anchorToFraction(anchor: string): number {
  const sec = parseDurationSec(anchor);
  if (sec == null) return 1;
  return 1 - Math.min(1, sec / RAIL_SPAN_SEC);
}

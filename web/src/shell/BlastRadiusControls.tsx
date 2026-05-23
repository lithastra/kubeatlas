/* ============================================================
 * BlastRadiusControls — bottom-center depth + direction toolbar.
 *
 * Two chip rows sitting over the canvas at the bottom: depth
 * choices (1 / 2 / 3 / 5 / ∞) and direction (↓ downstream, ↑
 * upstream, ↕ both). Mirrors the design's depth slider + mode
 * toggle (the radial menu primitive is queued for a separate pass).
 * Active only while BlastRadiusContext.active.
 * ============================================================ */
import { Box, Stack } from '@mui/material';

import { Panel } from '../design';
import type { BlastDirection } from '../lib/blastRadius';
import { useBlastRadius } from './BlastRadiusContext';

const DEPTHS: Array<{ value: number; label: string }> = [
  { value: 1, label: '1' },
  { value: 2, label: '2' },
  { value: 3, label: '3' },
  { value: 5, label: '5' },
  { value: Infinity, label: '∞' },
];

const DIRECTIONS: Array<{ value: BlastDirection; label: string }> = [
  { value: 'downstream', label: '↓' },
  { value: 'upstream', label: '↑' },
  { value: 'both', label: '↕' },
];

export function BlastRadiusControls() {
  const { active, depth, direction, setDepth, setDirection } = useBlastRadius();
  if (!active) return null;
  return (
    <Box
      sx={{
        position: 'absolute',
        bottom: 'var(--atlas-space-4)',
        left: '50%',
        transform: 'translateX(-50%)',
        zIndex: 4,
      }}
    >
      <Panel variant="card" padding={2} ariaLabel="Blast radius controls">
        <Stack direction="row" spacing={3} alignItems="center">
          <ChipGroup
            label="depth"
            options={DEPTHS}
            value={depth}
            onChange={setDepth}
          />
          <ChipGroup
            label="mode"
            options={DIRECTIONS}
            value={direction}
            onChange={setDirection}
          />
        </Stack>
      </Panel>
    </Box>
  );
}

interface ChipGroupProps<T extends string | number> {
  label: string;
  options: Array<{ value: T; label: string }>;
  value: T;
  onChange: (next: T) => void;
}

function ChipGroup<T extends string | number>({
  label,
  options,
  value,
  onChange,
}: ChipGroupProps<T>) {
  return (
    <Stack direction="row" spacing={0.5} alignItems="center">
      <Box
        component="span"
        sx={{
          fontFamily: 'var(--atlas-font-mono)',
          fontSize: 10,
          color: 'var(--atlas-text-3)',
          letterSpacing: '0.04em',
          textTransform: 'uppercase',
          mr: 0.5,
        }}
      >
        {label}
      </Box>
      {options.map((opt) => {
        const isActive = opt.value === value;
        return (
          <Box
            key={String(opt.value)}
            component="button"
            type="button"
            onClick={() => onChange(opt.value)}
            aria-pressed={isActive}
            sx={{
              minWidth: 28,
              padding: '4px 8px',
              border: '1px solid',
              borderColor: isActive ? 'var(--atlas-select)' : 'var(--atlas-border)',
              backgroundColor: isActive
                ? 'color-mix(in srgb, var(--atlas-select) 18%, transparent)'
                : 'transparent',
              fontFamily: 'var(--atlas-font-mono)',
              fontSize: 12,
              color: isActive ? 'var(--atlas-select)' : 'var(--atlas-text-2)',
              cursor: 'pointer',
              '&:hover': { borderColor: 'var(--atlas-select)' },
              '&:focus-visible': {
                outline: '2px solid var(--atlas-select)',
                outlineOffset: 1,
              },
            }}
          >
            {opt.label}
          </Box>
        );
      })}
    </Stack>
  );
}

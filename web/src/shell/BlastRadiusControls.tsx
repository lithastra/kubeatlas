/* ============================================================
 * BlastRadiusControls — in-panel depth + direction toolbar.
 *
 * Two wrapping chip rows next to the captured result: depth
 * choices (1 / 2 / 3 / 5 / 10) and an explicit server relation. Mirrors the design's depth slider + mode
 * toggle (the radial menu primitive is queued for a separate pass).
 * Active only while BlastRadiusContext.active.
 * ============================================================ */
import { Box, Stack } from '@mui/material';

import { Panel } from '../design';
import type { ImpactRelation } from '../api/impactTypes';
import { useBlastRadius } from './BlastRadiusContext';

const DEPTHS: Array<{ value: number; label: string }> = [
  { value: 1, label: '1' },
  { value: 2, label: '2' },
  { value: 3, label: '3' },
  { value: 5, label: '5' },
  { value: 10, label: '10' },
];

const DIRECTIONS: Array<{ value: ImpactRelation; label: string }> = [
  { value: 'dependents', label: 'Dependents' },
  { value: 'dependencies', label: 'Dependencies' },
];

export function BlastRadiusControls() {
  const { active, depth, direction, setDepth, setDirection } = useBlastRadius();
  if (!active) return null;
  return (
    <Box
      sx={{
        minWidth: 0,
      }}
    >
      <Panel variant="card" padding={2} ariaLabel="Impact analysis controls">
        <Stack spacing={1} alignItems="flex-start">
          <ChipGroup
            label="depth"
            options={DEPTHS}
            value={depth}
            onChange={setDepth}
          />
          <ChipGroup
            label="relation"
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
    <Stack direction="row" spacing={0.5} alignItems="center" flexWrap="wrap" useFlexGap>
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
              minHeight: 36,
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

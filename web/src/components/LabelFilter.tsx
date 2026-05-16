import { useState } from 'react';
import { Autocomplete, Chip, Stack, TextField } from '@mui/material';
import { useTranslation } from 'react-i18next';

import { useLabels } from '../api/labels';

// LabelFilter is the F-114 label-filter control for the Topology
// page. It is a controlled component: `value` is the active filter
// (one value per key — the server's filter is a flat map), and
// `onChange` is called with the next map whenever a filter is added
// or removed.
//
// The key / value pickers are populated from GET /api/v1/labels, so
// an operator browses the cluster's actual label vocabulary instead
// of guessing keys. Picking a value adds the filter immediately;
// active filters render as deletable chips.
export interface LabelFilterProps {
  value: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
}

export function LabelFilter({ value, onChange }: LabelFilterProps) {
  const { t } = useTranslation('translation');
  const { data, isLoading } = useLabels();
  const [pendingKey, setPendingKey] = useState<string | null>(null);

  const stats = data?.labels ?? [];
  const keyOptions = stats.map((s) => s.key);
  const valueOptions =
    pendingKey != null
      ? (stats.find((s) => s.key === pendingKey)?.values ?? []).map((v) => v.value)
      : [];

  const addFilter = (val: string | null) => {
    if (pendingKey == null || !val) return;
    onChange({ ...value, [pendingKey]: val });
    setPendingKey(null);
  };
  const removeFilter = (key: string) => {
    const next = { ...value };
    delete next[key];
    onChange(next);
  };

  return (
    <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap" useFlexGap>
      <Autocomplete
        sx={{ width: 220 }}
        size="small"
        options={keyOptions}
        value={pendingKey}
        loading={isLoading}
        onChange={(_, k) => setPendingKey(k)}
        renderInput={(params) => <TextField {...params} label={t('filter.label.key')} />}
      />
      <Autocomplete
        // Remount when the key changes so the value input clears
        // after a filter is added (pendingKey resets to null).
        key={pendingKey ?? '_none'}
        sx={{ width: 220 }}
        size="small"
        options={valueOptions}
        value={null}
        disabled={pendingKey == null}
        onChange={(_, v) => addFilter(v)}
        renderInput={(params) => <TextField {...params} label={t('filter.label.value')} />}
      />
      {Object.entries(value).map(([k, v]) => (
        <Chip
          key={k}
          label={`${k}=${v}`}
          onDelete={() => removeFilter(k)}
          size="small"
          color="primary"
        />
      ))}
    </Stack>
  );
}

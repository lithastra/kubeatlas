import { Tab, Tabs } from '@mui/material';
import { useTranslation } from 'react-i18next';

import type { Level } from '../api/types';

// LevelTabs is a thin MUI Tabs wrapper that emits the four
// aggregation-level constants used by the API. Workload and resource
// tabs are visible but disabled when their required scope (kind +
// name) isn't selected — clicking them with a kind/name picker not
// wired up wouldn't do anything useful.
export interface LevelTabsProps {
  value: Level;
  onChange: (level: Level) => void;
  // disable the per-resource-tab(s) when the scope isn't selected
  disableWorkload?: boolean;
  disableResource?: boolean;
}

export function LevelTabs({
  value,
  onChange,
  disableWorkload = false,
  disableResource = false,
}: LevelTabsProps) {
  const { t } = useTranslation('glossary');
  return (
    <Tabs
      value={value}
      onChange={(_, v) => onChange(v as Level)}
      aria-label="aggregation level"
    >
      <Tab value="cluster" label={t('level.cluster')} />
      <Tab value="namespace" label={t('level.namespace')} />
      <Tab value="workload" label={t('level.workload')} disabled={disableWorkload} />
      <Tab value="resource" label={t('level.resource')} disabled={disableResource} />
    </Tabs>
  );
}

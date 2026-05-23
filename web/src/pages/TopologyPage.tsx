import { useEffect, useRef, useState } from 'react';
import { Alert, Box, CircularProgress } from '@mui/material';
import { useTranslation } from 'react-i18next';

import { useGraph } from '../api/graph';
import type { Level } from '../api/types';
import { LabelFilter } from '../components/LabelFilter';
import { LevelTabs } from '../components/LevelTabs';
import { NamespacePicker } from '../components/NamespacePicker';
import { NodeDetailPanel } from '../components/NodeDetailPanel';
import { TopologyView, type TopologyControls } from '../components/TopologyView';
import { Panel } from '../design';
import { useRightPanel, ZoomScaleWidget } from '../shell';
import { useAppSelector } from '../store';

// TopologyPage is the cartography graph view. The canvas fills the
// shell's centre region; selection feeds the shell's right context
// panel (via useRightPanel) so the design's one-graph-many-modes
// pattern works without a separate detail route.
export function TopologyPage() {
  const { t } = useTranslation('translation');
  const [level, setLevel] = useState<Level>('cluster');
  const [labelFilter, setLabelFilter] = useState<Record<string, string>>({});
  const namespace = useAppSelector((s) => s.filter.namespace);
  const { setContent } = useRightPanel();
  const [zoom, setZoom] = useState(1);
  const controlsRef = useRef<TopologyControls | null>(null);

  const params =
    level === 'cluster'
      ? { level: 'cluster' as const, labels: labelFilter }
      : { level: 'namespace' as const, namespace: namespace ?? undefined, labels: labelFilter };
  const { data, isLoading, isError, error } = useGraph(params);

  // Clear the right panel when leaving the topology page.
  useEffect(() => () => setContent(null), [setContent]);

  const handleSelect = (id: string | null) => {
    setContent(id ? <NodeDetailPanel nodeId={id} /> : null);
  };

  return (
    <Box
      sx={{
        position: 'absolute',
        inset: 0,
        display: 'flex',
        flexDirection: 'column',
      }}
    >
      {/* Floating filter strip — sits over the canvas top-left so it
          doesn't carve space out of the graph viewport. */}
      <Panel
        variant="card"
        padding={2}
        sx={{
          position: 'absolute',
          top: 'var(--atlas-space-3)',
          left: 'var(--atlas-space-3)',
          zIndex: 5,
          maxWidth: 480,
        }}
      >
        <Box sx={{ display: 'flex', gap: 2, alignItems: 'center', flexWrap: 'wrap' }}>
          <LevelTabs value={level} onChange={setLevel} disableWorkload disableResource />
          {level === 'namespace' && <NamespacePicker />}
          <LabelFilter value={labelFilter} onChange={setLabelFilter} />
        </Box>
      </Panel>

      {level === 'namespace' && !namespace ? (
        <CenteredOverlay>
          <Alert severity="info">{t('filter.namespace.all')}</Alert>
        </CenteredOverlay>
      ) : isLoading ? (
        <CenteredOverlay>
          <CircularProgress size={28} />
        </CenteredOverlay>
      ) : isError ? (
        <CenteredOverlay>
          <Alert severity="error">{(error as Error)?.message ?? 'unknown error'}</Alert>
        </CenteredOverlay>
      ) : (
        <>
          <TopologyView
            view={data}
            onSelect={handleSelect}
            onZoom={setZoom}
            onReady={(c) => {
              controlsRef.current = c;
              setZoom(c.currentZoom());
            }}
          />
          <ZoomScaleWidget
            zoom={zoom}
            nodeCount={data?.nodes.length}
            onPickLevel={(targetZoom) => controlsRef.current?.zoomTo(targetZoom)}
          />
        </>
      )}
    </Box>
  );
}

function CenteredOverlay({ children }: { children: React.ReactNode }) {
  return (
    <Box
      sx={{
        flexGrow: 1,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
      }}
    >
      {children}
    </Box>
  );
}

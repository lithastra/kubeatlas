import { useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Box, CircularProgress, FormControlLabel, Switch, Tooltip } from '@mui/material';
import { useTranslation } from 'react-i18next';

import { useFederationGraph } from '../api/federation';
import { useGraph } from '../api/graph';
import { useOtelOverlay } from '../api/otel';
import { mergeOverlayEdges } from '../lib/overlay';
import type { Level } from '../api/types';
import { LabelFilter } from '../components/LabelFilter';
import { LevelTabs } from '../components/LevelTabs';
import { NamespacePicker } from '../components/NamespacePicker';
import { BlastRadiusPanel } from '../components/BlastRadiusPanel';
import { DiffChangeLog } from '../components/DiffChangeLog';
import {
  EdgeTypeFilter,
  EDGE_PRESET_TYPES,
  type EdgeFilterPreset,
} from '../components/EdgeTypeFilter';
import { NodeDetailPanel } from '../components/NodeDetailPanel';
import { TopologyView, type TopologyControls } from '../components/TopologyView';
import { Panel } from '../design';
import { CompassWidget, useBlastRadius, useClusterSelection, useDiffMode, useRightPanel, ZoomScaleWidget } from '../shell';
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
  const blast = useBlastRadius();
  const diff = useDiffMode();
  const cluster = useClusterSelection();
  const [zoom, setZoom] = useState(1);
  const [edgePreset, setEdgePreset] = useState<EdgeFilterPreset>('all');
  const [overlayOn, setOverlayOn] = useState(false);
  const controlsRef = useRef<TopologyControls | null>(null);

  // Fetch dispatch. Two paths share the same View shape so the rest
  // of the page is unchanged:
  //
  //   - Federated path (`cluster.selected != null`):
  //       useFederationGraph hits /api/v1/federation/graph?cluster=…
  //       — every node carries a clusterId so the cytoscape
  //       per-cluster border-tint rule lights up.
  //   - Single-cluster path (`cluster.selected == null`):
  //       useGraph hits /api/v1alpha1/graph at cluster or namespace
  //       level (the v1.3.0 behaviour). Honours the label filter +
  //       LevelTabs picker.
  //
  // Both hooks are always mounted; React Query disables the inactive
  // one via its `enabled` flag (federation: enabled iff cluster set;
  // useGraph: enabled iff scope is complete). Single-cluster installs
  // never see the federated path because cluster.selected stays null
  // (the picker has no member to pick).
  const fedQuery = useFederationGraph({
    clusters: cluster.selected ? [cluster.selected] : [],
    level: 'resource',
  });
  const params =
    level === 'cluster'
      ? { level: 'cluster' as const, labels: labelFilter }
      : { level: 'namespace' as const, namespace: namespace ?? undefined, labels: labelFilter };
  const localQuery = useGraph(params);
  const { data, isLoading, isError, error } = cluster.selected ? fedQuery : localQuery;

  // F-204 OTel overlay. Namespace-scoped and single-cluster only — the
  // correlator infers workload→workload runtime edges within a
  // namespace, which only line up with nodes at the namespace level.
  // The observed CALLS_AT_RUNTIME edges are layered onto the
  // declarative view for rendering (mergeOverlayEdges is non-
  // destructive; invariant 2.2 keeps them out of the graph proper).
  const overlayActive = overlayOn && level === 'namespace' && Boolean(namespace) && !cluster.selected;
  const overlayQuery = useOtelOverlay(namespace ?? '', overlayActive);
  const viewData = useMemo(() => {
    if (!data) return data;
    if (overlayActive && overlayQuery.data) return mergeOverlayEdges(data, overlayQuery.data.edges);
    return data;
  }, [data, overlayActive, overlayQuery.data]);

  // Clear the right panel when leaving the topology page.
  useEffect(() => () => setContent(null), [setContent]);

  // While blast-radius mode is active, the right panel shows the
  // hop-by-hop summary instead of the single-node detail. Restores
  // the detail view (for the root) on exit so the operator doesn't
  // lose their selection.
  useEffect(() => {
    if (diff.active && diff.anchor) {
      setContent(<DiffChangeLog anchor={diff.anchor} namespace={namespace ?? ''} />);
      return;
    }
    if (blast.active && blast.rootId) {
      setContent(
        <BlastRadiusPanel
          view={data}
          rootId={blast.rootId}
          depth={blast.depth}
          direction={blast.direction}
        />,
      );
    } else if (blast.rootId) {
      setContent(<NodeDetailPanel nodeId={blast.rootId} />);
    }
  }, [
    blast.active,
    blast.rootId,
    blast.depth,
    blast.direction,
    diff.active,
    diff.anchor,
    namespace,
    data,
    setContent,
  ]);

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
          <EdgeTypeFilter value={edgePreset} onChange={setEdgePreset} />
          {level === 'namespace' && namespace && (
            <Tooltip title={t('otel.overlay.hint')}>
              <FormControlLabel
                sx={{ ml: 0 }}
                control={
                  <Switch
                    size="small"
                    checked={overlayOn}
                    onChange={(e) => setOverlayOn(e.target.checked)}
                    inputProps={{ 'aria-label': t('otel.overlay.toggle') }}
                  />
                }
                label={t('otel.overlay.toggle')}
              />
            </Tooltip>
          )}
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
            view={viewData}
            onSelect={handleSelect}
            onZoom={setZoom}
            onReady={(c) => {
              controlsRef.current = c;
              setZoom(c.currentZoom());
            }}
            visibleEdgeTypes={EDGE_PRESET_TYPES[edgePreset]}
          />
          <ZoomScaleWidget
            zoom={zoom}
            nodeCount={data?.nodes.length}
            onPickLevel={(targetZoom) => controlsRef.current?.zoomTo(targetZoom)}
          />
          <CompassWidget />
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

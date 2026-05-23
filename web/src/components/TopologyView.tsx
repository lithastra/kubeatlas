import { useEffect, useRef } from 'react';
import { Box } from '@mui/material';
import type { Core, EventObject } from 'cytoscape';

import type { EdgeType, View } from '../api/types';
import { applyAtlasPalette, createCytoscape, paletteFor, updateCytoscape } from '../lib/cytoscape';
import { computeBlastRadius } from '../lib/blastRadius';
import { useSnapshotDiff } from '../api/snapshots';
import { useBlastRadius, useDiffMode, useSearchOverlay } from '../shell';
import { useAtlasTheme } from '../theme';

// TopologyView renders one cytoscape canvas using the cartography
// stylesheet. Lifecycle is direct (no React wrapper, per spec):
// create on mount, update via cy.json on view change, destroy on
// unmount. ResizeObserver re-fits on container size change. A theme
// change rebuilds the stylesheet in-place via applyAtlasPalette so
// selection state survives the swap.
//
// onSelect fires when the operator taps a node; null fires when the
// canvas background is tapped (the conventional "clear selection"
// gesture). Callers route the selection into the right context
// panel via useRightPanel().
//
// onZoom fires (rate-limited by cy) whenever zoom changes — the
// shell's ZoomScaleWidget consumes this to keep its scale chip in
// sync with the canvas. onReady hands back a controls handle once
// cytoscape has mounted so callers can animate zoom programmatically
// from the widget without poking at cyRef themselves.
export interface TopologyControls {
  zoomTo: (level: number) => void;
  currentZoom: () => number;
}

export interface TopologyViewProps {
  view: View | undefined;
  height?: number | string;
  onSelect?: (nodeId: string | null) => void;
  onZoom?: (zoom: number) => void;
  onReady?: (controls: TopologyControls) => void;
  // Edge-type allow-list. null means show every edge; a set means
  // edges with a `type` outside the set get the `dimmed` flag and
  // nodes that become orphaned (no visible edges) get dimmed too.
  visibleEdgeTypes?: ReadonlySet<EdgeType> | null;
}

const ZOOM_ANIM_MS = 400;

export function TopologyView({
  view,
  height = '100%',
  onSelect,
  onZoom,
  onReady,
  visibleEdgeTypes = null,
}: TopologyViewProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const cyRef = useRef<Core | null>(null);
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  const onZoomRef = useRef(onZoom);
  onZoomRef.current = onZoom;

  const { name: themeName } = useAtlasTheme();
  const { matchedIds } = useSearchOverlay();
  const blast = useBlastRadius();
  const diff = useDiffMode();
  // Diff fetch lives in the canvas effect because the decoration is
  // a per-node flag that depends on the API result. The right panel
  // (DiffChangeLog) issues the same query — React Query dedupes by
  // key so it's one network call.
  const { data: diffData } = useSnapshotDiff(diff.anchor ?? '', 'now', '');

  // Effect 1: create / update cytoscape from props.view.
  useEffect(() => {
    if (!containerRef.current || !view) return;
    if (cyRef.current) {
      updateCytoscape(cyRef.current, view);
    } else {
      const cy = createCytoscape(containerRef.current, view, paletteFor(themeName));
      cyRef.current = cy;
      // Node tap → emit selection. Background tap → clear.
      cy.on('tap', 'node', (ev: EventObject) => {
        onSelectRef.current?.(String(ev.target.id()));
      });
      cy.on('tap', (ev: EventObject) => {
        if (ev.target === cy) onSelectRef.current?.(null);
      });
      // Zoom broadcast for the ZoomScaleWidget. Fires on every
      // scroll/pinch tick; the consumer can debounce if needed.
      cy.on('zoom', () => onZoomRef.current?.(cy.zoom()));
      // Initial zoom + controls handoff (after the first fit settles).
      onZoomRef.current?.(cy.zoom());
      onReady?.({
        zoomTo: (level: number) =>
          cy.animate({ zoom: level, center: { eles: cy.elements() } }, { duration: ZOOM_ANIM_MS }),
        currentZoom: () => cy.zoom(),
      });
    }
    // theme name is intentionally NOT a dependency here — we want
    // theme changes to flow through effect 4 (live palette swap)
    // without re-running create/update.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view]);

  // Effect 2: fit on container resize.
  useEffect(() => {
    if (!containerRef.current) return;
    const el = containerRef.current;
    const obs = new ResizeObserver(() => {
      cyRef.current?.resize();
      cyRef.current?.fit(undefined, 24);
    });
    obs.observe(el);
    return () => obs.disconnect();
  }, []);

  // Effect 3: destroy cytoscape on unmount.
  useEffect(() => {
    return () => {
      cyRef.current?.destroy();
      cyRef.current = null;
    };
  }, []);

  // Effect 4: live palette swap on theme change — keeps selection.
  useEffect(() => {
    if (cyRef.current) {
      applyAtlasPalette(cyRef.current, paletteFor(themeName));
    }
  }, [themeName]);

  // Effect 5: project the ⌘K palette's matched IDs onto the canvas
  // as a `match` data flag. The stylesheet's node[?match] rule does
  // the rest — palette closes, matches stay highlighted (the
  // search-folds-into-graph IA principle).
  useEffect(() => {
    const cy = cyRef.current;
    if (!cy) return;
    cy.batch(() => {
      cy.nodes().forEach((n) => {
        const id = String(n.id());
        const isMatch = matchedIds.has(id);
        if (isMatch) n.data('match', true);
        else n.removeData('match');
      });
    });
  }, [matchedIds, view]);

  // Effect 7: diff-mode decoration. Translates the snapshot diff
  // (DiffEntry { namespace, kind, name }) into node-id matches
  // against the current canvas. Sets `added`/`removed`/`modified`
  // data flags; the stylesheet's node[?…] rules handle the look.
  useEffect(() => {
    const cy = cyRef.current;
    if (!cy) return;
    cy.batch(() => {
      cy.nodes().forEach((n) => {
        n.removeData('added');
        n.removeData('removed');
        n.removeData('modified');
      });
      if (!diff.active || !diffData) return;
      const idFor = (ns: string, kind: string, name: string) => `${ns || '_'}/${kind}/${name}`;
      for (const e of diffData.added ?? []) {
        const n = cy.getElementById(idFor(e.namespace, e.kind, e.name));
        if (n.length) n.data('added', true);
      }
      for (const e of diffData.removed ?? []) {
        const n = cy.getElementById(idFor(e.namespace, e.kind, e.name));
        if (n.length) n.data('removed', true);
      }
      for (const e of diffData.modified ?? []) {
        const n = cy.getElementById(idFor(e.namespace, e.kind, e.name));
        if (n.length) n.data('modified', true);
      }
    });
  }, [diff.active, diffData, view]);

  // Effect 6: composite dim/brighten. Blast-radius mode wins when
  // active (every non-reachable node/edge dims, the reachable
  // subgraph stays bright). When blast is off, the edge-type
  // filter takes over: edges with a `type` outside the allow-list
  // dim, and nodes that lose every visible incident edge dim too.
  // No active mode → wipe all dimmed flags so the canvas snaps
  // back to normal.
  useEffect(() => {
    const cy = cyRef.current;
    if (!cy) return;
    cy.batch(() => {
      if (blast.active && blast.rootId && view) {
        const result = computeBlastRadius(view, blast.rootId, blast.direction, blast.depth);
        const reachable = result.reachable;
        cy.nodes().forEach((n) => {
          if (reachable.has(String(n.id()))) n.removeData('dimmed');
          else n.data('dimmed', true);
        });
        cy.edges().forEach((e) => {
          const inSet =
            reachable.has(String(e.source().id())) && reachable.has(String(e.target().id()));
          if (inSet) e.removeData('dimmed');
          else e.data('dimmed', true);
        });
        return;
      }
      if (visibleEdgeTypes) {
        const visibleNodeIds = new Set<string>();
        cy.edges().forEach((e) => {
          const t = e.data('type') as EdgeType | undefined;
          const ok = t != null && visibleEdgeTypes.has(t);
          if (ok) {
            e.removeData('dimmed');
            visibleNodeIds.add(String(e.source().id()));
            visibleNodeIds.add(String(e.target().id()));
          } else {
            e.data('dimmed', true);
          }
        });
        cy.nodes().forEach((n) => {
          if (visibleNodeIds.has(String(n.id()))) n.removeData('dimmed');
          else n.data('dimmed', true);
        });
        return;
      }
      cy.nodes().removeData('dimmed');
      cy.edges().removeData('dimmed');
    });
  }, [blast.active, blast.rootId, blast.depth, blast.direction, view, visibleEdgeTypes]);

  return (
    <Box
      ref={containerRef}
      data-testid="topology-canvas"
      sx={{
        width: '100%',
        height,
        // Transparent — the GridBackground beneath shows through.
        backgroundColor: 'transparent',
      }}
    />
  );
}

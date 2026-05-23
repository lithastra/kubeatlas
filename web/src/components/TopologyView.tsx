import { useEffect, useRef } from 'react';
import { Box } from '@mui/material';
import type { Core, EventObject } from 'cytoscape';

import type { View } from '../api/types';
import { applyAtlasPalette, createCytoscape, paletteFor, updateCytoscape } from '../lib/cytoscape';
import { useSearchOverlay } from '../shell';
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
}

const ZOOM_ANIM_MS = 400;

export function TopologyView({ view, height = '100%', onSelect, onZoom, onReady }: TopologyViewProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const cyRef = useRef<Core | null>(null);
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  const onZoomRef = useRef(onZoom);
  onZoomRef.current = onZoom;

  const { name: themeName } = useAtlasTheme();
  const { matchedIds } = useSearchOverlay();

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

import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { Box } from '@mui/material';
import type { Core, EventObject, NodeSingular } from 'cytoscape';

import type { EdgeType, View } from '../api/types';
import { RadialMenu, type RadialMenuOption } from '../design';
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
  // Right-click radial menu state: viewport coords + the node id
  // the menu acts on. null when closed. The cxttap handler below
  // populates it inside the create-cytoscape branch.
  const [radial, setRadial] = useState<{ x: number; y: number; nodeId: string } | null>(null);
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
      // Right-click on a node opens the RadialMenu at the click
      // position; the menu picks the blast-radius depth in one
      // gesture. originalEvent is the underlying MouseEvent (cy
      // augments it with rendered coords); we use clientX/Y to
      // anchor the menu in viewport space.
      cy.on('cxttap', 'node', (ev: EventObject) => {
        const me = ev.originalEvent as MouseEvent | undefined;
        if (!me) return;
        me.preventDefault?.();
        setRadial({
          x: me.clientX,
          y: me.clientY,
          nodeId: String(ev.target.id()),
        });
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

  // Keyboard traversal. The canvas is focusable (tabIndex 0) so a
  // screen-reader / keyboard-only operator can land on it via Tab.
  // Once focused:
  //
  //   ArrowRight / ArrowDown → next node in id-sorted order
  //   ArrowLeft  / ArrowUp   → previous node
  //   Enter / Space          → open the focused node in the right
  //                            detail panel (same path as a click)
  //   Escape                 → clear focus / selection (unless
  //                            blast / diff mode is on; those modes
  //                            own Esc via the shell-level handler)
  //
  // The traversal walks the flat id-sorted list — a deterministic,
  // edge-agnostic order that screen-reader users can predict. A
  // direction-aware (geometric) traversal is a future enhancement;
  // this one is good enough for the M6.1 a11y starter.
  const onCanvasKeyDown = useCallback(
    (e: KeyboardEvent<HTMLDivElement>) => {
      const cy = cyRef.current;
      if (!cy) return;
      const nodes = cy.nodes().sort((a, b) => String(a.id()).localeCompare(String(b.id())));
      if (nodes.length === 0) return;

      const currentId = String(cy.$('node:selected').first().id() ?? '');
      const currentIdx = nodes.toArray().findIndex((n) => String(n.id()) === currentId);

      const focus = (idx: number) => {
        const target = nodes[idx] as NodeSingular;
        cy.elements().unselect();
        target.select();
        cy.animate({ center: { eles: target }, zoom: cy.zoom() }, { duration: 200 });
      };

      switch (e.key) {
        case 'ArrowRight':
        case 'ArrowDown': {
          e.preventDefault();
          focus(currentIdx < 0 ? 0 : (currentIdx + 1) % nodes.length);
          break;
        }
        case 'ArrowLeft':
        case 'ArrowUp': {
          e.preventDefault();
          focus(currentIdx < 0 ? nodes.length - 1 : (currentIdx - 1 + nodes.length) % nodes.length);
          break;
        }
        case 'Enter':
        case ' ': {
          if (currentIdx < 0) return;
          e.preventDefault();
          onSelectRef.current?.(String(nodes[currentIdx].id()));
          break;
        }
        case 'Escape': {
          if (blast.active || diff.active) return; // shell owns Esc
          if (currentIdx >= 0) {
            e.preventDefault();
            cy.elements().unselect();
            onSelectRef.current?.(null);
          }
          break;
        }
      }
    },
    [blast.active, diff.active],
  );

  // Options for the right-click radial: pick a blast-radius depth.
  // Selecting any option enters blast-radius mode on the right-
  // clicked node at that depth (downstream direction, the default).
  const radialOptions: RadialMenuOption[] = radial
    ? [
        {
          id: 'd1',
          label: '1 hop',
          onSelect: () => {
            blast.enter(radial.nodeId);
            blast.setDepth(1);
          },
        },
        {
          id: 'd3',
          label: '3 hops',
          onSelect: () => {
            blast.enter(radial.nodeId);
            blast.setDepth(3);
          },
        },
        {
          id: 'dinf',
          label: '∞',
          onSelect: () => {
            blast.enter(radial.nodeId);
            blast.setDepth(Infinity);
          },
        },
        {
          id: 'cancel',
          label: '×',
          onSelect: () => {
            /* close-only, no side effect */
          },
        },
      ]
    : [];

  return (
    <>
    <Box
      ref={containerRef}
      data-testid="topology-canvas"
      // role="application" tells screen readers that this region
      // takes keyboard input directly (cytoscape canvas isn't a
      // standard widget). tabIndex=0 puts it in the tab order so
      // operators can land on it without a pointing device.
      role="application"
      aria-label="Topology graph. Arrow keys to traverse, Enter to open, Escape to clear."
      tabIndex={0}
      onKeyDown={onCanvasKeyDown}
      sx={{
        width: '100%',
        height,
        // Transparent — the GridBackground beneath shows through.
        backgroundColor: 'transparent',
        outline: 'none',
        '&:focus-visible': {
          // Soft inset ring so the focus state is visible without
          // disrupting the cartography aesthetic — same select hue
          // the rest of the chrome uses.
          boxShadow: 'inset 0 0 0 2px var(--atlas-select)',
        },
      }}
    />
    <RadialMenu
      open={radial != null}
      anchor={radial ? { x: radial.x, y: radial.y } : null}
      options={radialOptions}
      onClose={() => setRadial(null)}
      label="Blast radius depth"
    />
    </>
  );
}

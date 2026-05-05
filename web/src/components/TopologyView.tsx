import { useEffect, useRef } from 'react';
import { Box } from '@mui/material';
import type { Core } from 'cytoscape';

import type { View } from '../api/types';
import { createCytoscape, updateCytoscape } from '../lib/cytoscape';

// TopologyView renders one cytoscape canvas. The component manages
// the cytoscape lifecycle directly (no cytoscape-react wrapper, per
// spec): create on mount, update on graph change via cy.json (a
// structural diff that's an order of magnitude cheaper than
// destroy+recreate), destroy on unmount.
//
// A ResizeObserver re-fits the layout when the container changes
// size (sidebar toggles, window resize, etc.).
export interface TopologyViewProps {
  view: View | undefined;
  height?: number | string;
}

export function TopologyView({ view, height = '70vh' }: TopologyViewProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const cyRef = useRef<Core | null>(null);

  // Effect 1: create / update cytoscape from props.view.
  useEffect(() => {
    if (!containerRef.current || !view) return;
    if (cyRef.current) {
      updateCytoscape(cyRef.current, view);
    } else {
      cyRef.current = createCytoscape(containerRef.current, view);
    }
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

  return (
    <Box
      ref={containerRef}
      sx={{
        width: '100%',
        height,
        backgroundColor: 'background.paper',
        border: 1,
        borderColor: 'divider',
        borderRadius: 1,
      }}
    />
  );
}

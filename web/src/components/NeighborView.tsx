import { useEffect, useRef, useState } from 'react';
import { Alert, Box, CircularProgress } from '@mui/material';

import { renderMermaid } from '../lib/mermaid';

let renderCounter = 0;

export interface NeighborViewProps {
  // mermaidText is the server-generated flowchart string (View.mermaid
  // from the resource-level aggregator). Empty when the view exceeds
  // the renderer cap; the parent should show a "too many neighbors"
  // hint and link to the topology view in that case.
  mermaidText: string;
}

// NeighborView renders a single Mermaid flowchart. We use the async
// mermaid.render API (rather than mermaid.run on a class selector) so
// we don't depend on the order React inserts elements, and so concurrent
// detail-pane mounts don't collide on element ids.
//
// SVG insertion goes through DOMParser + appendChild instead of
// innerHTML: defence-in-depth against any future regression in
// mermaid's own sanitiser. The server-side renderer already escapes
// problematic chars (#quot;, #lt;, #gt;, #124;) before the text ever
// hits this component.
export function NeighborView({ mermaidText }: NeighborViewProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(true);

  useEffect(() => {
    if (!mermaidText) {
      setPending(false);
      return;
    }
    let cancelled = false;
    setPending(true);
    setError(null);
    const id = `neighbor-svg-${renderCounter++}`;
    renderMermaid(id, mermaidText)
      .then((svg) => {
        if (cancelled || !containerRef.current) return;
        const doc = new DOMParser().parseFromString(svg, 'image/svg+xml');
        const root = doc.documentElement;
        // Clear and re-attach.
        const c = containerRef.current;
        while (c.firstChild) c.removeChild(c.firstChild);
        c.appendChild(root);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (!cancelled) setPending(false);
      });
    return () => {
      cancelled = true;
    };
  }, [mermaidText]);

  if (!mermaidText) {
    return null;
  }
  return (
    <Box>
      {pending && (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 2 }}>
          <CircularProgress size={20} />
        </Box>
      )}
      {error && <Alert severity="error">{error}</Alert>}
      <Box
        ref={containerRef}
        sx={{
          '& svg': { maxWidth: '100%', height: 'auto' },
        }}
      />
    </Box>
  );
}

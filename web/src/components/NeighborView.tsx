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
        // Post-process: Mermaid v11 sometimes sizes node boxes
        // slightly narrower than the rendered label, clipping
        // long names like HorizontalPodAutoscaler/podinfo. Walk
        // every .node, measure the actual label width, and grow
        // the foreignObject + shape (rect / polygon path bbox)
        // to fit. Runs on a rAF so layout has settled.
        requestAnimationFrame(() => {
          if (!c) return;
          const NODE_PADDING = 24; // 12px each side
          c.querySelectorAll('g.node').forEach((node) => {
            const label = node.querySelector<HTMLElement>('.nodeLabel');
            const fo = node.querySelector<SVGForeignObjectElement>('foreignObject');
            if (!label || !fo) return;
            const textWidth = Math.ceil(label.getBoundingClientRect().width);
            const wanted = textWidth + NODE_PADDING;
            const have = parseFloat(fo.getAttribute('width') ?? '0');
            if (wanted <= have) return;
            const delta = wanted - have;
            // Widen the foreignObject and re-center it.
            const x = parseFloat(fo.getAttribute('x') ?? '0');
            fo.setAttribute('width', String(wanted));
            fo.setAttribute('x', String(x - delta / 2));
            // Widen any rect sibling (rectangle/round-rectangle nodes).
            node.querySelectorAll('rect').forEach((rect) => {
              const w = parseFloat(rect.getAttribute('width') ?? '0');
              if (w <= 0) return;
              const rx = parseFloat(rect.getAttribute('x') ?? '0');
              rect.setAttribute('width', String(w + delta));
              rect.setAttribute('x', String(rx - delta / 2));
            });
          });
        });
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
          overflowX: 'auto',
          // Mermaid v11 emits svg width="100%" even with
          // useMaxWidth=false. Override every level so the SVG
          // renders at its natural intrinsic size (driven by node
          // box widths), then the container scrolls if needed.
          '& svg': {
            display: 'block',
            width: 'auto !important',
            height: 'auto !important',
            maxWidth: 'none !important',
          },
          // Mermaid v11 wraps each htmlLabel in
          //   <foreignObject><div class="nodeLabel"><p>…</p></div></foreignObject>
          // The default styles wrap the <p> text at the foreignObject's
          // computed width and round it down — long labels clip.
          // Force nowrap on the label DOM so the layout engine picks
          // a foreignObject width that actually fits.
          '& .nodeLabel, & .nodeLabel *, & .label, & .label *': {
            whiteSpace: 'nowrap !important',
            overflow: 'visible !important',
            textOverflow: 'clip !important',
          },
          '& foreignObject': { overflow: 'visible' },
        }}
      />
    </Box>
  );
}

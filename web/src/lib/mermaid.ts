import mermaid from 'mermaid';

// Single mermaid initialisation across the app. `securityLevel:
// 'strict'` disables HTML in labels and blocks script execution
// inside any foreignObject the renderer emits — the server already
// escapes problematic chars too, so flipping `htmlLabels: true`
// is safe and required for correct label sizing on long names
// (the SVG-text fallback mismeasures node-box widths and clips
// strings like "HorizontalPodAutoscaler/podinfo"). `theme:
// 'default'` is the flat light palette that coordinates with our
// MUI theme.
let initialized = false;
function ensureInit() {
  if (initialized) return;
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: 'strict',
    theme: 'default',
    flowchart: {
      htmlLabels: true,
      // Render at the diagram's natural width so node boxes stay
      // sized to their labels. useMaxWidth=true scales the whole
      // SVG to fit the container, which also shrinks the boxes
      // and causes long names like HorizontalPodAutoscaler/podinfo
      // to clip even though htmlLabels measured them correctly.
      // The NeighborView container handles overflow.
      useMaxWidth: false,
      nodeSpacing: 40,
      rankSpacing: 60,
      padding: 12,
    },
  });
  initialized = true;
}

// renderMermaid produces an SVG string for the given flowchart text.
// The id must be unique per call so concurrent renders don't collide
// in mermaid's internal element-id namespace.
export async function renderMermaid(id: string, text: string): Promise<string> {
  ensureInit();
  const { svg } = await mermaid.render(id, text);
  return svg;
}

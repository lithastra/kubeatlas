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
      useMaxWidth: true,
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

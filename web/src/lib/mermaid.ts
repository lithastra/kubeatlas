import mermaid from 'mermaid';

// Single mermaid initialisation across the app. `securityLevel:
// 'strict'` disables HTML in labels (the server already escapes
// problematic chars; defence in depth). `theme: 'default'` is the
// flat light palette that coordinates with our MUI theme.
let initialized = false;
function ensureInit() {
  if (initialized) return;
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: 'strict',
    theme: 'default',
    flowchart: {
      htmlLabels: false,
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

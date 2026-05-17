# 0012 — Server-side SVG / PNG rendering

- Status: accepted
- Date: 2026-05-17
- Task: P3-T14 (F-115)

## Context

F-115 adds `/api/v1/export?format=svg|png` — a server-side render
of a cluster or namespace view into a static image. The use case
is dropping a dependency graph into a runbook, an incident doc, or
a slide without a running browser.

The Web UI renders the graph with Cytoscape, and Cytoscape can
export SVG — but only in the browser, against a live DOM. The
export endpoint runs inside the API process: no DOM, no browser.
It needs a headless rendering path.

KubeAtlas already emits Graphviz DOT (`pkg/graph/dot.go`,
`ToDOTOptions`) for the legacy CLI and the DOT artefact, so a
graph → image path that starts from DOT reuses code that already
exists and is already tested.

Four approaches were considered.

### A. Headless Chromium

Bundle Chromium (via puppeteer or chromedp) and snapshot the actual
Cytoscape UI.

- **+** Pixel-identical to what the user sees in the browser.
- **−** Adds 200 MB+ to the image and a large, frequently-patched
  attack surface for one endpoint.
- **−** A browser process per render is the heaviest possible
  answer to "draw a graph".

### B. `gonum.org/v1/plot` + a hand-written layout

Lay the graph out in pure Go and draw it with gonum/plot.

- **+** Pure Go, no external binary, no image-size cost.
- **−** Graph layout is the hard part. A hand-rolled layout will
  not approach Graphviz's `dot` ranked-DAG quality, and a
  dependency graph that is hard to read defeats the feature.

### C. DOT + the Graphviz `dot` CLI

Render the existing DOT output to SVG/PNG by shelling out to
`dot -Tsvg` / `dot -Tpng`.

- **+** Reuses `pkg/graph/dot.go` unchanged — DOT is already the
  project's graph serialisation.
- **+** `dot` is a mature, stable, native binary; its ranked-DAG
  layout is the quality bar the other options are measured against.
- **+** ~20 MB added to the image — an order of magnitude less
  than Chromium.
- **−** A non-Go runtime dependency the image must carry (see
  Consequences).

### D. A pure-Go DOT library (`gographviz` etc.)

- **−** `gographviz` parses and builds DOT graphs in memory; it
  does not render them to an image. It solves a different problem.
  Rejected outright.

## Decision

**Adopt C — DOT + the Graphviz `dot` CLI.**

- `pkg/graph/render.go` adds `ToSVG` / `ToPNG`. Each calls
  `ToDOTOptions` for the DOT string, then pipes it through
  `dot -T<format>` via `os/exec` — DOT on stdin, the image on
  stdout, no temp files, so `readOnlyRootFilesystem` is preserved.
- If the `dot` binary is not on `PATH`, render returns a typed
  error the handler turns into `503 Service Unavailable` with a
  link to the install docs — the endpoint degrades, the process
  does not crash, and the rest of the API keeps serving.
- The endpoint lives beside the other read endpoints in `pkg/api`;
  it renders the current graph state and never triggers Rego
  evaluation.

## Consequences

- **Runtime image.** The goreleaser runtime image moves off
  `gcr.io/distroless/static-debian12` — which has no package
  manager and cannot carry `dot` — to a Debian slim base with
  `graphviz` installed via `apt-get`. `runAsNonRoot` and
  `readOnlyRootFilesystem` are kept: `dot` reads stdin and writes
  stdout, so it needs no writable filesystem. Invariant 2.7 (no
  cloud-provider SDKs) is unaffected — `graphviz` is a local
  rendering CLI, not a cloud SDK.
- **Abuse / OOM guard.** `dot` is CPU- and memory-bound, so the
  export endpoint is throttled: a small fixed number of renders
  may run concurrently and excess requests get `429 Too Many
  Requests` immediately; any view larger than 1000 nodes is
  refused with `413` and the caller is told to narrow by namespace.
- **Formats.** SVG and PNG only. JPEG / WebP / PDF are out of
  scope — a caller who needs PDF can `convert` an SVG locally.

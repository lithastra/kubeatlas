---
sidebar_position: 7
title: CLI reference
---

# CLI reference

The `kubeatlas` binary is the same in every install — server, one-shot
modes, and stand-alone subcommands all live in a single executable.
Run it without arguments to start the API server; pass a subcommand
to invoke an offline tool.

## Subcommands

### `kubeatlas export`

Walks the cluster the current `KUBECONFIG` points at, runs every
built-in extractor, and emits the result as a Graphviz DOT
document. Pipe through `dot -Tsvg`, `dot -Tpng`, or any other
Graphviz output format.

```bash
kubeatlas export --format=dot > graph.dot
dot -Tsvg graph.dot -o graph.svg
```

#### Flags

| Flag | Default | Notes |
|---|---|---|
| `--format` | `dot` | Only `dot` is supported. Pipe through Graphviz for other formats. SVG, PNG, and Mermaid are all reachable through `dot -T...`. |
| `--namespace` | `""` (whole cluster) | Restrict the rendered graph to one namespace. Cross-namespace edges are dropped when one endpoint is filtered out. |
| `--output` | `""` (stdout) | Write to a file instead of stdout. |
| `--title` | `KubeAtlas` | Override the digraph identifier. Useful when generating multiple SVGs into a single dashboard. |

#### Examples

Render only the `petclinic` namespace:

```bash
kubeatlas export --format=dot --namespace=petclinic | dot -Tsvg > petclinic.svg
```

Generate a PNG with a custom title:

```bash
kubeatlas export --format=dot --title="prod cluster" \
  --output=/tmp/prod.dot
dot -Tpng /tmp/prod.dot -o /tmp/prod.png
```

Pipe directly into image-processing toolchains:

```bash
kubeatlas export --format=dot | dot -Tdot:cairo | base64 | gh gist create -
```

#### What's *not* supported (on purpose)

- `--format=svg` / `--format=png` — `export` emits DOT only. For a
  rendered image use `kubeatlas -once -format=svg`, pipe `export`
  through `dot -Tsvg` / `dot -Tpng`, or call the
  `GET /api/v1/export` endpoint.
- `--format=mermaid` — Mermaid was retired on the API side at v1.0,
  and the CLI follows the same path.
- `--server=URL` — not implemented yet. The default cluster-direct
  discovery covers the common case (operator's laptop pointing at
  any cluster); a future release may add this for "render what the
  running KubeAtlas already saw" workflows.

### `kubeatlas diagnose`

Scans the cluster the current `KUBECONFIG` points at and writes a
self-contained [diagnostic report](./concepts/diagnose-report) — the
dependency graph plus the orphan, cycle, and top blast-radius
analyses — as a single HTML document or as JSON. Like `export`, it
runs offline: no KubeAtlas server in the cluster is required.

```bash
# Whole-cluster HTML report
kubeatlas diagnose --all-namespaces --output cluster-report.html

# One namespace, as JSON for a pipeline
kubeatlas diagnose --namespace petclinic --format json > petclinic.json
```

#### Flags

| Flag | Default | Notes |
|---|---|---|
| `--namespace`, `-n` | `""` (whole cluster) | Restrict the report to one namespace. Mutually exclusive with `--all-namespaces`. |
| `--all-namespaces` | `false` | Report every namespace (whole-cluster scope). |
| `--format` | `html` | `html` (self-contained document) or `json` (structured, `jq`-friendly). |
| `--output`, `-o` | `""` (stdout) | Write to a file instead of stdout. |
| `--context`, `--kubeconfig` | current context, `$KUBECONFIG` | Select the cluster for the scan. |

The HTML report is fully self-contained — inline CSS and SVG, no CDN,
no web fonts — so it opens with no network access, which is the point
for air-gapped review. The graph image is rendered with the graphviz
`dot` binary; when `dot` is absent the report still renders in full,
with a notice in place of the image.

#### Examples

Produce a report for a CI run and attach it as an artifact:

```bash
kubeatlas diagnose --all-namespaces --format html --output report.html
```

Assert in a pipeline that a namespace has no orphaned resources:

```bash
kubeatlas diagnose -n petclinic --format json | jq -e '.orphans | length == 0'
```

The same report is available from a running server at
`GET /api/v1/diagnose?namespace=<ns>&format=html|json`.

### `kubeatlas rules-test`

Offline rule-pack evaluator for rule-pack contributors. See
[Rego rules](./concepts/rego-rules) for the workflow.

## Top-level flags (server mode)

When invoked without a subcommand, `kubeatlas` runs the API server
and the informer pipeline. The most common flags:

| Flag | Notes |
|---|---|
| `--once` | Single offline discovery pass — talk to the Kubernetes API directly (no KubeAtlas server), emit the graph, and exit. |
| `--format` | Output format for `--once`: `json` (default), `dot`, or `svg`. `svg` renders via the graphviz `dot` tool. |
| `--level`, `--namespace`, `--kind`, `--name` | Drive `--once` mode — the aggregation level and the resource selector. See `kubeatlas --help`. |
| `--context`, `--kubeconfig` | Select the cluster for local runs — the kubeconfig context and file. Default to the current context and `$KUBECONFIG`. |
| `--rule-pack <ref>` | Load an extra rule pack (OCI ref or local directory). Repeatable. The Helm chart sets `KUBEATLAS_RULE_PACKS` from `rulePacks.extras`. |
| `--version` | Print build metadata and exit. |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Usage / connection / I/O error. The CLI prints a one-line message to stderr. |
| 2 | `rules-test` only — at least one fixture produced no edges or errored. The per-sample report is still emitted on stdout. |

## `kubectl-atlas` plugin

`kubectl-atlas` is a separate binary — the KubeAtlas `kubectl`
plugin. Placed on `PATH`, `kubectl` exposes it as `kubectl atlas`.
It shows a KubeAtlas view of a resource, a namespace, or the whole
cluster:

```bash
kubectl atlas deployment api -n petclinic   # one resource
kubectl atlas namespace petclinic           # a namespace
kubectl atlas cluster                       # the whole cluster
```

### Modes

The plugin runs in three modes — the first two need no KubeAtlas
server in the cluster:

| Mode | Selected by | What it does |
|---|---|---|
| Offline | *(default)* | Builds the graph from the Kubernetes API and renders a static SVG file locally. Needs the graphviz `dot` tool on `PATH`. |
| Local UI | `--local-ui` | Runs a KubeAtlas server in-process and opens the interactive web UI. No graphviz, no in-cluster server. Holds until `Ctrl-C`. |
| Online | `--online`, `--server`, `KUBEATLAS_URL` | Opens a live in-cluster KubeAtlas UI. The URL is resolved from `--server`, then `KUBEATLAS_URL`, then a `kubectl port-forward` to the in-cluster Service. |

Offline mode is self-contained — it renders in-process and does not
need the `kubeatlas` binary on `PATH`.

### Flags

| Flag | Notes |
|---|---|
| `--online` | Use a running in-cluster KubeAtlas server instead of rendering offline. |
| `--server <url>` | KubeAtlas UI base URL — implies `--online`. |
| `--local-ui` | Offline: serve the interactive UI from an in-process server instead of rendering an SVG. |
| `--host <addr>` | Bind address for the `--local-ui` server. Default `127.0.0.1`; use `0.0.0.0` to expose it on the network. |
| `--kubeatlas-namespace <ns>` | Namespace KubeAtlas is installed in, for online port-forward discovery. Default `kubeatlas`. |
| `-n`, `--namespace <ns>` | Namespace of the target resource. |
| `--context`, `--kubeconfig` | Select the cluster — the kubeconfig context and file. |
| `--no-browser` | Print the URL (or file path) instead of opening a browser. |

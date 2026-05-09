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

- `--format=svg` — embedding cgo Graphviz bindings would bloat the
  binary on every install. Use `dot -Tsvg` post-process; the
  Graphviz dependency is small and ubiquitous.
- `--format=mermaid` — Mermaid was retired on the API side at v1.0,
  and the CLI follows the same path.
- `--server=URL` — not implemented yet. The default cluster-direct
  discovery covers the common case (operator's laptop pointing at
  any cluster); a future release may add this for "render what the
  running KubeAtlas already saw" workflows.

### `kubeatlas rules-test`

Offline rule-pack evaluator for rule-pack contributors. See
[Rego rules](rego-rules) for the workflow.

## Top-level flags (server mode)

When invoked without a subcommand, `kubeatlas` runs the API server
and the informer pipeline. The most common flags:

| Flag | Notes |
|---|---|
| `--once` | Single discovery pass, write JSON + DOT, exit. Used by integration tests and PoC harnesses. |
| `--rule-pack <ref>` | Load an extra rule pack (OCI ref or local directory). Repeatable. The Helm chart sets `KUBEATLAS_RULE_PACKS` from `rulePacks.extras`. |
| `--version` | Print build metadata and exit. |
| `--level`, `--namespace`, `--kind`, `--name` | Drive `--once` mode. See `kubeatlas --help`. |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | Usage / connection / I/O error. The CLI prints a one-line message to stderr. |
| 2 | `rules-test` only — at least one fixture produced no edges or errored. The per-sample report is still emitted on stdout. |

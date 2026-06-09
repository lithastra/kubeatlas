---
sidebar_position: 8
title: Diagnostic report
---

# Diagnostic report

The **diagnostic report** bundles everything KubeAtlas knows
about a cluster — or a single namespace — into one portable artifact:
the dependency graph plus the orphan, cycle, and blast-radius analyses,
captured at a point in time.

It exists for the moments when the interactive UI isn't an option: an
air-gapped audit, a CI artifact attached to a pipeline run, or a
periodic snapshot filed for the record.

## What's in it

| Section | Answers |
|---|---|
| **Dependency graph** | The scoped graph, rendered as inline SVG. The whole picture in one image. |
| **Orphans** | Resources nothing depends on that shouldn't be roots — leftover ReplicaSets, dangling ConfigMaps. See [Orphans & cycles](./orphan-cycle). |
| **Cycles** | Dependency loops, each tagged with a category (`bootstrap-cert`, `intentional`, `unknown`). |
| **Top blast radius** | The ten resources the most other resources transitively depend on — *"delete this and the most breaks"*. See [Blast radius](./blast-radius). |
| **Header** | Scope, generation time, and the KubeAtlas version that produced the report. |

The report shows **relationships and risk**, not raw manifests. It is
deliberately not a `kubectl get -o yaml` dump — for that, use `kubectl`.

## Two formats

- **HTML** (`--format html`, the CLI default) — a single self-contained
  document. CSS is inlined, the graph is an inline SVG, and there are
  **no external resources of any kind**: no CDN, no web fonts, no
  analytics. It opens in any browser with no network access, which is
  the whole point for air-gapped review. It is also dark-mode aware
  (`prefers-color-scheme`).
- **JSON** (`--format json`) — the same data, structured for
  automation. Pipe it through `jq`, diff two reports, or assert on it
  in CI.

## Producing one

Offline, from the CLI — no running KubeAtlas server required:

```bash
# Whole-cluster HTML report
kubeatlas diagnose --all-namespaces --output cluster-report.html

# One namespace, as JSON for a pipeline
kubeatlas diagnose --namespace petclinic --format json > petclinic.json
```

Or from a running server, over the API:

```bash
curl -fsS "http://localhost:8080/api/v1/diagnose?namespace=petclinic&format=json" | jq
curl -fsS "http://localhost:8080/api/v1/diagnose?namespace=petclinic&format=html" > report.html
```

See the [CLI reference](../cli-reference) for every flag.

## Graphviz is optional

The graph image is rendered with the graphviz `dot` binary. When `dot`
isn't on `PATH` — a common air-gapped case — the report still renders
in full: every other section is independent of graphviz, and a short
notice takes the place of the image. A missing graphviz never produces
an empty report.

## Use cases

- **Air-gapped audit** — generate the HTML on a connected jump host (or
  in-cluster) and carry the single file into the secured environment.
  It needs nothing but a browser.
- **CI artifact** — emit `--format html` (or JSON) in a pipeline and
  attach it to the run, so every deploy leaves a dependency-graph
  record.
- **Periodic snapshot** — schedule the CLI to file a report on a cadence
  and keep the series for after-the-fact "what did the graph look like
  on the day it broke?" investigations.

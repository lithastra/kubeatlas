---
sidebar_position: 7
title: Performance
---

# Performance

KubeAtlas serves its read API from a [graph store](./blast-radius.md)
that comes in two tiers — **Tier 1** (in-memory) and **Tier 2**
(PostgreSQL + Apache AGE). Both are benchmarked against synthetic
stress fixtures so a release can be checked for regressions.

## Latency targets

The `cluster` and `namespace` views and the blast-radius walk are
the hot read paths. The v1.2 service-level targets, p95:

| Endpoint | Target (p95) |
|----------|--------------|
| cluster-view (`/graph?level=cluster`) | ≤ 1000 ms |
| namespace-view (`/graph?level=namespace`) | ≤ 1000 ms |
| blast-radius | ≤ 500 ms |

These were briefly relaxed to 5–8 s during the v1.0 work, while every
cluster/namespace view materialised the whole graph in the API
process per request. The store-side **pushdown aggregation** moved
that work into the store (a `GROUP BY` on Tier 2, a single pass on
Tier 1), so v1.2 reclaims the original targets.

## Baselines

Captured with `test/perf/bench-v1.sh` (100 samples per endpoint) on
Docker Desktop Kubernetes under WSL2. Tier 2 numbers include
`kubectl port-forward` + WSL2 round-trip overhead; Tier 1 is a local
memory-backed process. **p95, milliseconds:**

| Endpoint | 5K T1 | 5K T2 | 10K T1 | 10K T2 |
|----------|------:|------:|-------:|-------:|
| cluster-view   |  7.6 | 145.0 |  29.1 |   360.2 |
| namespace-view | 36.9 | 696.1 | 390.8 | 1433.9 ⚠ |
| blast-radius   |  0.7 |   5.9 |   0.7 |     9.3 |

- **5K** — the `stress-test-5k` fixture (~7.2K resources), the v1.0
  / v1.1 baseline (`perf-baseline-v1.0.json`,
  `perf-baseline-v1.1.json`).
- **10K** — the `stress-test-10k` fixture (~14.4K resources), the
  v1.2 baseline (`perf-baseline-v1.2.json`).
- **Multi-cluster (v1.3)** — two-cluster federation fixture
  captured by `test/perf/start-multicluster-fixture.sh`. The
  `/api/v1/federation/graph?level=cluster` path returns in 6 ms
  p50 (vs. 2177 ms for the resource-level merge) — a 200×
  reduction from the store-side cluster-summary aggregator added
  in v1.3.0. Numbers in `perf-baseline-v1.3.json`.

cluster-view and blast-radius stay well inside target on both tiers
at both scales. blast-radius is near-flat because a recursive-CTE
traversal is bounded by the affected subgraph, not the total graph.

⚠ **The one over-target cell — 10K Tier 2 namespace-view (1.43 s).**
This is a known artefact of the synthetic fixture, not a product
regression. `stress-test-10k` puts *all* ~10K resources in a single
namespace; on Tier 2 the `NamespaceSubgraph` JOIN then costs more
than a full scan, because the namespace filter selects 100 % of the
rows. Real namespaces are a small slice of a cluster, where the
JOIN is a clear win — and the realistic whole-graph aggregation,
cluster-view, lands at 360 ms p95. Tier 1 (in-memory) is unaffected.
The synthetic single-namespace fixture is deliberately not optimised
for; see `perf-baseline-v1.2.json` for the recorded numbers.

## Regression gate

`test/verify/perf-regression.sh` re-runs the benchmark and fails CI
if any metric, on any tier, against either the v1.0 or the v1.2
baseline, regresses by more than 20 %:

```bash
# capture a fresh run per fixture, then:
bash test/verify/perf-regression.sh
```

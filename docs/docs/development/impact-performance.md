---
title: v1.7 impact query performance preparation
---

# v1.7 impact query performance preparation

Status: preliminary local Tier 1 evidence, **not M4 candidate acceptance**.
The accepted [impact contract](./impact-analysis.md) requires a frozen 5K shape
and at least 100 samples before evaluating the new-query p95 target of one
second. This benchmark supplies a reproducible local shape. Dense, 10K, Tier 2,
real-cluster workloads and final-candidate evidence remain separate.

## Frozen local workload

`BenchmarkImpactAPI5K` creates exactly 5,000 synthetic standalone resources:
1,000 ConfigMaps, 1,000 Deployments, 1,000 ReplicaSets and 2,000 Pods in namespace
`bench`. It creates 6,000 edges: 1,000 Deployment-to-ConfigMap references, 1,000
ReplicaSet-to-Deployment ownership edges, 2,000 Pod-to-ReplicaSet ownership
edges and 2,000 Pod-to-ConfigMap references. Mean stored edge density is 1.2.
There are no real Kubernetes objects, Secret values, credentials or API calls.

The fixed query selects `cm-0000`, relation `dependents`, depth 5, limit 200,
no expected UID and no cluster parameter. Its oracle is four returned resources
with no truncation. Resource UIDs and reference-field details are unknown;
this fixture does not establish informer coverage, ownership-instance matching
or field-level provenance. The single-cluster Tier 1 store reads all resources
and edges for each consistent projection.

The benchmark sends sequential real loopback HTTP GETs through the same
recovery, metrics, version metrics, access-log and CORS middleware chain as
`Server.Start`. Timing covers response-body receipt and JSON decoding/checking.
It excludes fixture construction and ten warm-up GETs per benchmark invocation.
The normal Go one-iteration calibration invocation is separate from the final
100 measured samples. There is no model, cache tuning, cluster deployment or
persistent workload. The server is closed when the benchmark exits.

## Observed result

On the connected Mac mini (Mac16,10, Apple M4, Darwin arm64, 10 logical CPUs,
16 GiB physical memory), the final production-middleware run on October 3,
2026 produced:

| Metric | Result |
| --- | --- |
| Measured requests | 100 |
| Mean request duration | 4.971 ms |
| Nearest-rank p95 | 5.461 ms |
| Returned resources per response | 4 |
| Non-200, read, decode or oracle failures | 0 |

These local numbers are below the proposed one-second target for this shape.
They do **not** close the M4 gate: this was an unfrozen development revision,
a sparse fixture with four matches, no competing readers/writers, no informer
or history integration, no production network/TLS and no Tier 2 database.
Hardware isolation, CPU frequency, container memory and long-duration resource
growth were not measured. Additional fixed synthetic shapes are recorded below; real-cluster cases remain unmeasured.
A preliminary run without access-log middleware measured p95 5.327 ms; it is
retained as preparation only and is not the final result above.

## Reproduce

From the main repository:

```bash
go test ./pkg/api -run '^$' -bench '^BenchmarkImpactAPI5K$' -benchtime=100x -count=1
```

Record commit, Go version, hardware, complete benchmark output and whether other
workloads were active. Keep results for different shapes and storage tiers
separate. A failed response is a failed measurement, never an omitted sample.
The benchmark reports p95 from the measured duration array; it does not infer
p95 from Go's average `ns/op`.

The benchmark and focused Go regression changes passed CI-pinned
`golangci-lint v2.11.4` locally. The draft PR retains M4 upgrade, supported
Kubernetes versions, compatibility, Secret sentinel, immutable artifacts and a
complete 72-hour observation as independent requirements. No soak was launched.

## Additional bounded preparation shapes

All three use ten warm-ups and 100 measured requests with production middleware,
on the same Mac. These are development measurements, not frozen acceptance.

| Benchmark | Resources | Edges | Oracle | Mean | p95 |
| --- | --- | --- | --- | --- | --- |
| `BenchmarkImpactAPI5K` | 5,000 | 6,000 | 4, complete | 5.499 ms | 6.146 ms |
| `BenchmarkImpactAPI10K` | 10,000 | 12,000 | 4, complete | 10.859 ms | 12.420 ms |
| `BenchmarkImpactAPI5KHighFanout` | 5,000 | 18,000 | 200, truncated | 12.776 ms | 13.610 ms |

The 10K shape doubles the sparse groups. The high-fanout shape references five
shared ConfigMaps from every Deployment and Pod; 4,000 matches are reachable,
with the fixed response limit of 200. No budget or response limit was relaxed.
An earlier exploratory 5K/63K-edge shape returned HTTP 413 under the existing
16 MiB projection budget. That failure remains a limitation; the bounded
18K-edge case does not supersede it or establish universal dense-graph support.

```bash
go test ./pkg/api -run '^$' -bench '^BenchmarkImpactAPI(5K|10K|5KHighFanout)$' -benchtime=100x -count=1
```

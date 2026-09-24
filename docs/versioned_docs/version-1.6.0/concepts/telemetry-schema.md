---
sidebar_position: 9
title: Telemetry schema
---

# Telemetry schema

KubeAtlas can send **anonymous, opt-in** usage telemetry. It is **off by
default** — you choose to turn it on. This page documents exactly what
is sent, why, how long it is kept, and how to turn it off. Nothing is
hidden: the same payload is available live at
`GET /api/v1/telemetry/preview`.

## The contract

- **Opt-in.** `telemetry.enabled` defaults to `false`. Telemetry sends
  nothing until you set it `true`.
- **Coarse and anonymous.** Only the fields below — no resource names,
  namespaces, label values, IPs, install UUIDs, or any identifier that
  could correlate two sessions.
- **One fixed endpoint.** Reports go to
  `https://telemetry.kubeatlas.dev/v1/report`, operated by the project.
  The endpoint is hard-coded in the binary and **not** configurable —
  you cannot redirect telemetry to a third party.
- **Fire-and-forget.** A report is sent once a day over HTTPS with a
  10-second timeout. Failures are logged and counted
  (`kubeatlas_telemetry_send_errors_total`) but never retried and never
  affect the main path.

## Enabling it

```yaml
# values.yaml
telemetry:
  enabled: true
```

On startup the server logs a line naming the endpoint, the preview URL,
and how to disable. Check what would be sent first:

```bash
curl -fsS http://localhost:8080/api/v1/telemetry/preview | jq
```

## The payload

Schema version `1.0`. Retention at the receiver is **90 days**, after
which raw reports are deleted (only aggregates are kept).

| Field | Type | Why it's collected |
|---|---|---|
| `schema_version` | string | Lets the receiver parse the right shape. |
| `kubeatlas_version` | string | Which versions are in use → where to focus fixes and when an old version can be dropped. |
| `k8s_version` | string | Which Kubernetes versions to test against. |
| `os` / `arch` | string | Which platforms to build and test for. |
| `tier` | `memory` \| `postgres` | How many installs run Tier 2 → where to invest in persistence. |
| `resource_bucket` | `<1K` \| `1K-5K` \| `5K-10K` \| `>10K` | Order-of-magnitude graph size (never the exact count) → realistic perf targets. |
| `enabled_packs` | string[] | Which rule packs are used (names only, no versions or contents) → which to maintain. |
| `cluster_count` | integer | How common multi-cluster federation is. |
| `platform_distribution` | map | Cluster counts by platform family (counts only, no names). |
| `session_nonce` | string | Random per process start, **never persisted**. Lets the receiver de-duplicate one session's repeated reports **without** correlating across restarts. |

## What is never collected

Resource names · namespace names · label values · annotations · user or
ServiceAccount identity · IP addresses · install UUID · any value that
could link two sessions.

## Disabling it

```yaml
telemetry:
  enabled: false
```

or `helm upgrade ... --set telemetry.enabled=false`. With telemetry off
the code path never runs and the server makes no outbound connection to
the endpoint.

## Changing the schema

Any change to the fields above is a human-reviewed change: it must bump
`schema_version` and update this page in the same commit. New fields are
never added silently (guide invariant 2.3).

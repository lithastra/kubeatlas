---
sidebar_position: 6
title: Historical snapshots
---

# Historical snapshots

The live graph answers *"what does the cluster look like now?"*.
Snapshots answer the follow-up SREs actually ask during an
incident: *"what changes were observed?"*. KubeAtlas records resource
add, update, and delete observations in an append-only metadata stream.
The diff endpoint summarizes the retained observations in a time window;
it cannot establish that every Kubernetes change was collected.

## Tier 2 only

Snapshots are a **Tier 2** feature — they need the PostgreSQL
backend. The event stream is an append-only table; a Tier 1
in-memory install has nowhere durable to keep it, and a feature
that silently loses history on every Pod restart is worse than no
feature at all.

This is enforced, not just documented:

- The Helm chart's `values.schema.json` rejects
  `snapshots.enabled=true` unless `persistence.enabled=true`.
- The snapshot API endpoints return **`503 Service Unavailable`**
  on any install where the writer is not running — Tier 1, or
  Tier 2 with `snapshots.enabled=false`.

The memory backend does carry a tiny fixed ring buffer of recent
events, but that exists only so the unit tests have something to
exercise. It is not a Tier 1 feature and is not exposed through the
API.

## How it works

```
informer event ──▶ Writer.Enqueue ──▶ bounded queue ──▶ worker pool ──▶ PostgreSQL
   (hot path)        (non-blocking)                       (N workers)    resource_events
```

The snapshot writer subscribes to the same informer pipeline that feeds the
live graph. Beginning with v1.5.2, every event is metadata-only: resource
payloads are cleared before entering the queue, and PostgreSQL enforces
`resource_events.data IS NULL`. The key property is that it **never blocks the
informer**:

- `Enqueue` is non-blocking. It drops the event onto a bounded
  channel and returns immediately.
- A pool of worker goroutines drains the channel into PostgreSQL in
  the background.
- If the queue is full — a burst arriving faster than the workers
  can write — the writer sheds the **oldest** queued event rather
  than stalling the informer. Every shed event increments
  `kubeatlas_snapshot_queue_drop_total`.

Dropping under overload protects the informer, but leaves a known history gap.
Monitor the queue and write-loss counters; a later successful write or periodic
marker cannot reconstruct a discarded event or prove a complete diff window.

<a id="full-sync-markers" />

### Observation markers

Alongside the event stream, KubeAtlas writes **snapshot markers** —
rows in `snapshot_meta` that record "at this timestamp the stored graph
held N observed resources". In v1.7 development, resource and edge counts and
marker append share one atomic stored-graph observation. A marker does not
contain an atomic historical graph or backup. The diff reads its supplied time window;
a marker neither fills missing events nor proves a recoverable snapshot.
Markers are written:

- **periodically**, by the optional Helm `CronJob`
  (`snapshots.fullSyncSchedule`), which calls the internal
  trigger endpoint;
- **manually**, via `kubeatlas snapshot trigger` or a direct
  `POST` to the internal trigger endpoint.

`GET /api/v1/snapshots` lists these markers, most-recent first. In v1.7
federation, the internal POST requires exactly one attached `cluster` and checks
member visibility before counting or writing. An edge counts only when both
endpoints exist in that member. Empty standalone scope counts only unlabelled
rows. Counts include reference-only graph nodes and do not prove current
Kubernetes state, drained history delivery, or complete retained history.

Snapshots must be explicitly enabled. The endpoint remains an internal operator
surface; read visibility alone is not write authorization. The existing trigger
CLI now accepts one `--cluster` and an optional `KUBEATLAS_TOKEN`. The Helm
CronJob can pass `snapshots.trigger.cluster` and an existing `tokenSecretRef`,
with no fan-out or Job retry. Unscoped v1.7 federation requests fail closed. A write error or timeout
may have an unknown outcome; there is no automatic retry or idempotency guarantee.
See [internal member markers](../installation/multicluster.md#internal-member-markers-v17-development)
for budgets, response codes and pending deployment qualification.

## Retention

The event stream is pruned to a configurable window —
`snapshots.retention`, default **7d**. An hourly background
retainer deletes every event older than the window in batched
`DELETE`s so the prune never holds a long transaction.

Retention bounds two things at once: the table's disk footprint,
and the **maximum diff window**. The diff endpoint rejects any
`from`/`to` window wider than the retention period — events beyond
the window have been pruned, so a wider diff would silently
under-report changes. Asking for more history than you keep is a
`400`, not a partial answer.

## Reading the diff

The Web UI surfaces snapshot diffs in two places:

- The **time-axis rail** at the top of the topology canvas — pick
  an anchor (preset chip or drag the rail itself) and the canvas
  enters diff mode: added resources gain a green halo, removed
  ones drop to a dashed muted outline, modified ones get an amber
  overlay. Esc clears.

  ![Topology canvas in diff mode — added / removed / modified nodes decorated against the live graph; right panel lists per-row changes with relative timestamps.](/img/topology-diffmode.png)

- The dedicated **Snapshots page** — a window-picker driven view
  with three tables (Added / Removed / Modified) plus a timeline
  of recent observation markers; the same data the API surfaces, in
  a denser tabular form.

  ![Snapshots page — window-picker presets, Added / Removed / Modified diff tables, snapshot-marker timeline.](/img/snapshots.png)

Under both, the wire format is the same:

`GET /api/v1/snapshots/diff?from=1h&to=now` replays the event
stream across the window and classifies every resource that
appears in it:

| Bucket | Meaning |
|---|---|
| `added` | First event in the window is an add; no later delete |
| `removed` | Last event in the window is a delete |
| `modified` | Seen, but neither freshly added nor removed |

`from` and `to` accept `now`, a duration read as "ago"
(`5m`, `1h`, `7d`), or an absolute RFC3339 timestamp. `to` defaults
to `now`. An optional `namespace` restricts the diff to one
namespace; omit it for the whole cluster.

The diff and its backing event table contain resource **identity and metadata** — kind,
namespace, name, UID — not full resource bodies. It tells you
*what* changed and *when*; reach for `kubectl` or your audit log
for the field-level *how*.

See the [API reference](../api-reference.md#snapshots-tier-2) for
the full request and response shapes.

## Member-scoped reads (v1.7 development)

In the unreleased v1.7 implementation, federation requests must explicitly select
one attached member with `cluster`, for example
`GET /api/v1/snapshots/diff?cluster=prod&from=1h`. Snapshot lists use the same
selector. Both routes enforce member visibility before reading history; the
unscoped v1alpha1 aliases do not provide a federation fallback. Standalone
queries select only unlabelled records, without attributing legacy observations
to a named member. The snapshot page and topology diff use the cluster strip:
select one member to enable reads; “all clusters” pauses them. Switching members
hides the previous results and clears member-specific highlights while loading.
The read-only `kubeatlas snapshot list` and `snapshot diff --from 1h` commands
accept `--cluster` for the same scope; `snapshot trigger` remains separate.

Reads have fixed row, metadata, time and concurrency limits; a truncated or
oversized read fails rather than returning a partial result. These summaries
classify retained observations, not original Kubernetes creation/deletion times.
A marker does not fill history gaps or prove a restorable snapshot. See
[scoped history reads](../installation/multicluster.md#scoped-history-reads-v17-development)
for scope, limits and compatibility details. This is development behavior, not a
change to the published v1.6 instructions below.

## Enabling snapshots

Install CloudNativePG chart 0.29.0 (operator 1.30.0) first as described in
[Persistence (Tier 2)](../installation/persistence.md#path-a-embedded-cloudnativepg),
then enable both persistence and snapshots:

```bash
helm upgrade --install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.6.0 \
  --namespace kubeatlas --create-namespace \
  --set persistence.enabled=true \
  --set persistence.embedded.enabled=true \
  --set snapshots.enabled=true \
  --set snapshots.retention=7d
```

Tuning knobs, all under `snapshots` in `values.yaml`:

| Value | Default | What it controls |
|---|---|---|
| `enabled` | `false` | Master switch (requires `persistence.enabled`) |
| `retention` | `7d` | Event-stream window and max diff width |
| `queueSize` | see chart | Writer queue capacity before drop-oldest kicks in |
| `workers` | see chart | Number of goroutines draining the queue into PG |
| `fullSyncSchedule` | see chart | Cron schedule for the periodic full-sync marker |

## Observability

The writer exports Prometheus metrics on `/metrics` when it is
running:

| Metric | Type | Meaning |
|---|---|---|
| `kubeatlas_snapshot_events_processed_total` | counter | Events durably written to PostgreSQL |
| `kubeatlas_snapshot_queue_drop_total` | counter | Events shed at enqueue under queue-full backpressure |
| `kubeatlas_snapshot_write_failed_total` | counter | Events dropped after the per-event retry budget was exhausted |
| `kubeatlas_snapshot_queue_depth` | gauge | Events currently buffered in the writer queue |

A healthy install shows `processed_total` climbing with cluster
churn and both drop counters flat at zero. A steadily rising
`queue_drop_total` means the queue cannot keep up — raise
`snapshots.workers` or `snapshots.queueSize`. A rising
`write_failed_total` instead points at PostgreSQL itself —
check the database's health and connectivity.

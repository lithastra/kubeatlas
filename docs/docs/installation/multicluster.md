---
sidebar_position: 10
title: Multi-cluster & RBAC visibility
---

# Multi-cluster federation

KubeAtlas can attach to several clusters at once and serve a **federated
graph** across them. Every resource is tagged with its `ClusterID`, so
the UI can group and colour by cluster and the API can return a merged
or per-cluster view.

## Enable federation

Federation reads a Secret whose **keys are cluster names** and whose
**values are kubeconfigs**:

```bash
kubectl -n kubeatlas create secret generic member-kubeconfigs \
  --from-file=prod=./prod.kubeconfig \
  --from-file=staging=./staging.kubeconfig
```

```yaml
# values.yaml
multicluster:
  enabled: true
  kubeconfigSecret: member-kubeconfigs
```

Each key becomes a `ClusterID`; it must not contain `:` or `/` (those
are reserved in the resource-ID prefix). Attachment is best-effort — a
single bad kubeconfig is logged and skipped, the rest still attach.

The federated surface is served under `/api/v1/federation/*`:

| Endpoint | Returns |
| --- | --- |
| `GET /api/v1/federation/clusters` | The attached member clusters. |
| `GET /api/v1/federation/graph?cluster=a,b` | A merged (or per-cluster, `level=cluster`) view across the named clusters. |

## Impact API inventory (v1.7 development)

In development source, each member informer periodically reads that member's
API discovery metadata using its own kubeconfig. The impact response includes
cached, cluster-scoped inventory evidence; **API observation details** in the
Web result shows its before/after state. These reads run in the background,
not when a user opens or exports an impact result.

Discovery uses the member credentials' existing discovery permissions. A
forbidden or failed discovery round is shown as such; it does not imply an
operator is absent or that a resource can be changed safely. Discovery metadata
is not proof of list/watch permission, successful observation, or effective
RBAC permissions. No Secret objects are requested by this worker, and no
additional collection permissions are needed for inventory. CRD/Gatekeeper
object collection has separate read-permission requirements described below.

Rounds retain the ten-second deadline, five-minute polling interval,
ten-minute expiry, and bounded endpoint inventory. Removing a member or
shutting down cancels and joins its worker; reattaching the name starts a new
inventory generation. v1.7 is unreleased and real deployment acceptance remains
pending.

## Member CRD and Gatekeeper collection (v1.7 development)

Each member bundle also runs CRD and Gatekeeper discovery using that member's
clients. Custom-resource instances and Constraints, their derived edges, and
source-coverage records retain the member cluster ID. Identical names in two
members do not share a graph identity or a Rego evaluation-cache entry.

Member credentials need read-only list/watch access to
`apiextensions.k8s.io/v1/customresourcedefinitions` and the custom-resource
endpoints being collected. Gatekeeper additionally needs discovery access to
its supported template API and list/watch access to
`templates.gatekeeper.sh/v1/constrainttemplates` and generated
`constraints.gatekeeper.sh/v1beta1` Constraint types. This feature does not
grant permissions or read Secret objects. A denied or unavailable endpoint
retains explicit discovery/list/watch coverage gaps; it is not a safe-change
or operator-absence assertion.

Removing a member or shutting down cancels all of its collectors and waits for
in-flight dynamic callbacks before the name can be reattached. Other members
continue independently.

Active canonical core, selected CRD and generated Constraint endpoints also run
background missed-delete repair after initial handler delivery and every five
minutes. One round at a time per member uses fresh paginated lists, a ten-second
deadline, page size 500, a 10,000-object bound and at most 32 pages. It requires
the same list permission as collection and issues no Secret request. Only a
complete valid list can authorize removal of a missing resource in that member
and exact Kind/API version. Atomic UID/resourceVersion checks preserve graph
rows that changed during the round; deletion cascades incident edges. Failed
rounds retain coverage gaps and later rounds can repair remaining rows without
clearing earlier evidence of a gap.

Different API versions, unregistered/retired endpoints, unversioned rows and
reference-only nodes are retained. Stopping a member or removing a CRD/template
does not itself sweep its instances. CRD version equivalence, complete collector
coverage, member history continuity and real deployment acceptance remain open.
Frozen-candidate deployment validation remains pending.

## Member retained history metadata (v1.7 development)

When Tier 2 storage and snapshots/history recording are enabled, each running
member bundle also owns a retained-metadata observer. It reads only that
member's first/last retained non-Secret event timestamps and latest stored
snapshot-marker timestamp within the configured retention window. Unlabelled
legacy rows and other members are excluded. There is no event-payload, resource
count or backup-content query in this feature.

Observers sample at startup and every five minutes. Each read has a five-second
deadline including waiting for a shared limit of two concurrent member reads.
At most 128 distinct member caches are retained, including stopped scopes.
An additional uncached member reports unsupported retention metadata without
stopping graph collection or evicting another member's cache. Removing a member
cancels and joins its observer; same-name reattachment replaces the old cache
with a fresh unqueried lifetime.

Authorized impact requests read the selected member's cache without a database
query or refresh. Evidence expires after ten minutes and retains independent
`stale`/`stopped` flags. `retainedData: observed` means only that its recorded
window was read successfully. Without a registered member recording session,
the parent writer state remains `unsupported` with
`history_cluster_scope_unavailable`. No global writer counters or start time
are attributed to that member.
Event bounds do not prove continuous history; a marker is not a backup or a
verified recovery point. Visibility of existing legacy history routes remains
separate work.

## Member recording state and loss (v1.7 development)

Tier 2 members with history enabled bind their core, CRD instance and Gatekeeper
constraint event sinks to one independent recording session per member. It uses
the existing shared queue, configured workers and retry budget; this feature does not create a queue or worker pool
for each member. Queue loss is attributed to the member whose event was
discarded. Exhausted writes are attributed to the event's member. Another
member's global drop/failure totals do not become this member's loss evidence.

Impact history state can now be `recording`, `degraded` or `stopped` for a
registered member session. Pending-delivery qualification includes that member's
in-flight writes. Known queue/write loss makes coverage partial and remains
sticky through later success and same-name reattachment; otherwise coverage is
unknown. Recording does not assert continuous history or complete source
coverage. CRD and Gatekeeper instance add/update/delete observations and their
committed missed-delete repairs now use the same member token. The separate
collectors do not create additional writer queues or workers.

Overlapping member collectors use the graph's persisted UID/resourceVersion
under their shared resource lock. An unchanged replay, same-version observation
from another collector, served-version switch or same-name reattachment does not
create another event. A served-version switch can still update the graph, and
replays still derive edges. Receipt-read/resource-write failures produce no
history; a later successful delivery can record the change. Deletes compare the
stored incarnation atomically; duplicate deletes and old-UID tombstones leave
replacement objects intact. Stopping a CRD/template registration retains its
instances and emits no synthetic delete. Missing UID/version metadata leaves a
processing gap and cannot qualify an event.

Events contain identity metadata only. They record observation/persistence time,
not the original Kubernetes creation/deletion time. Resource versions are opaque:
independent watches can deliver different versions out of order, and the graph
receipt does not provide a historical deduplication ledger or an ordered audit
stream. Graph persistence and queued history delivery are separate operations;
recording state and known loss remain qualified as above. Standalone dynamic
collectors are not enabled for history by this member-only change.

Member removal stops its collectors, then waits for its recording session's
queued/in-flight writes before releasing the name. The shared writer stays
available to other member sessions. A new lifetime has its own start/pending
state; old tokens cannot mutate it. Up to 128 distinct recording scopes retain
qualified process state, including stopped entries. Additional scopes continue
using the existing queue budget with unsupported member recording evidence.
Retained-metadata observations remain independent and cannot erase recording
loss, prove a lossless interval or verify a recovery point.

## RBAC visibility (F-206)

By default every caller can see every attached cluster. When you run one
KubeAtlas in front of clusters owned by different teams, you can restrict
**which clusters each caller sees** with a set of token rules.

:::info Read-side visibility only
This is a deliberately small, simplified multi-tenancy control. It
filters *which clusters a request may see* on the federation surface —
nothing more. It does **not** fetch or rotate cluster credentials, add
OIDC/SSO, or introduce any CRD. Deeper tenancy and credential lifecycle
are future decisions, not part of v1.5.
:::

Each rule maps a **bearer token** to a list of clusters:

```yaml
# values.yaml
multicluster:
  enabled: true
  kubeconfigSecret: member-kubeconfigs
  rbac:
    rules:
      - token: "REPLACE-team-a-token"
        clusters: [prod, prod-dr]
      - token: "REPLACE-team-b-token"
        clusters: [staging]
```

A caller then presents its token as a standard bearer header:

```bash
curl -H "Authorization: Bearer REPLACE-team-a-token" \
  http://kubeatlas:8080/api/v1/federation/clusters
# -> { "mode": "federated", "clusters": ["prod", "prod-dr"] }   (staging is hidden)
```

Behaviour:

- **No rules configured** → the federation surface is open to everyone,
  exactly as v1.4. Fully backward compatible.
- **Matched token** → the caller sees only that rule's clusters.
  `/federation/clusters` lists just those; `/federation/graph` serves
  them and **rejects** any other requested cluster with `403`.
- **No token, rules configured** → `401 Unauthorized`.
- **Unknown token** → `403 Forbidden`.

An unauthorised caller is always *rejected*, never handed a silently
empty list — so it can tell "denied" from "no clusters attached".

:::warning Handle tokens as secrets
Tokens are secret material. Supply them from a secret values source
(sealed-secrets, SOPS, `helm upgrade -f secret-values.yaml`) — never
commit plaintext tokens to a values file checked into git. The chart
renders them into a `<release>-rbac-rules` Secret mounted read-only into
the pod, and the process keeps only their SHA-256 hashes in memory.
:::

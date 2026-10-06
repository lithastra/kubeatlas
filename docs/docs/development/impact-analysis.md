---
title: v1.7 impact analysis contract
---

# v1.7 impact analysis contract

Status: **M0 design baseline, not a shipped feature**. The release owner accepted
the v1.7 direction on September 30, 2026. This document defines implementation
and acceptance work; it is not evidence that those checks already pass.

## Outcome and scope

A DevOps engineer can select a resource, identify its observed dependents,
inspect the relationship evidence, see the limits of the observation, and
export the same result for change review. The supported environment remains
vanilla Kubernetes, a single application replica, and Tier 1 or opt-in Tier 2.

This is current-state relationship analysis, **not** arbitrary manifest
simulation, historical topology reconstruction, an outage prediction, a
permission-use proof, or an automated remediation system.

The implementation sequence is M0 contract, M1 analysis core, M2 observation
coverage and API, M3 Web/CLI/report integration, and M4 candidate acceptance.
Correctness, visibility, Secret boundaries, and incomplete-data warnings are
not optional scope cuts. Alternative paths and presentation polish can wait.

## Existing behavior to preserve and replace deliberately

The legacy backend blast-radius query walks incoming edges in the store. Its
response contains a source, affected resources, a count, and a depth, but no
path evidence. The legacy Web helper walks the loaded, potentially aggregated
or filtered view: `downstream` follows outgoing edges, while `upstream` follows
incoming edges. Their labels, default depths, and data scopes are not a common
analysis contract.

The shared synthetic fixture in `test/fixtures/impact-analysis/reference-graph.json`
characterizes those existing implementations. For its ConfigMap root, the
backend and Web `upstream` find consumers, while Web `downstream` does not.
This is a measured semantic mismatch on that fixture, not a claim that every
production graph has been reproduced. Legacy tests are characterization, not
the desired naming or acceptance oracle for the new API.

The new feature uses **dependents** and **dependencies**, never an ambiguous
upstream/downstream label. Existing API routes and `/api/v1alpha1` byte shapes
stay unchanged. M3 replaces the product-facing local calculation with the new
server result; the old helper may remain only for explicitly local visual
exploration, not an authoritative impact count.

## Relationship contract

For a stored edge `from -> to`, dependents normally traverses `to -> from`;
dependencies traverses `from -> to`. Paths retain the original edge direction
and separately record the traversal direction. Direct means one traversal step;
indirect means more than one. The root is never included in result counts.

| Edge family | Observed evidence | Interpretation and limits |
| --- | --- | --- |
| `OWNS` | Child owner reference points to owner | Ownership relationship, not a guarantee of immediate deletion or failure; deletion policy/finalizers remain outside simulation. |
| `USES_CONFIGMAP` | Workload/Pod config reference | Distinguish environment, ordinary volume, and `subPath` when source evidence is available; optional references are conditional. |
| `USES_SECRET` | Non-Secret resource references a Secret name | Reference-only target; Secret existence, UID, contents, and validity are unknown. No Secret read is permitted. |
| `MOUNTS_VOLUME` | Workload/Pod references PVC | Storage dependency; no claim about provider durability or application behavior. |
| `SELECTS`, `ROUTES_TO`, `ATTACHED_TO` | Selector, backend, or parent reference | Declared routing/selection relationship, not an active endpoint or a verified traffic path. |
| `USES_SERVICEACCOUNT` | Workload/Pod service-account reference | Declared identity use, not proof that an operation was performed or authorized. |
| `BINDS_ROLE`, `BINDS_SUBJECT` | Binding roleRef and subjects | Authorization association; report separately from ordinary dependent counts. |
| Other rule/policy/platform edges | Stored edge and available provenance | Show as unmodeled relationships, not automatic outage propagation. OTel overlay edges do not enter this traversal. |

RBAC is a branching relation: `binding -> role` and `binding -> subject`.
Following only incoming edges from a Role reaches the binding, not its subject.
A separate, bounded authorization explanation may join Role, Binding,
ServiceAccount, and workload using those actual edges. Each step retains its
direction; this join must not imply effective permission, exclusive authority,
or runtime use. Do not traverse all edges undirected to manufacture that path.

An edge proves a declared relationship, not a failure. In particular, ConfigMap
environment variables do not update automatically, ordinary projected volumes
eventually update, and `subPath` mounts do not receive updates. Application
reload behavior is not known from those relationships alone. See the
[Kubernetes ConfigMap contract](https://kubernetes.io/docs/concepts/configuration/configmap/#mounted-configmaps-are-updated-automatically).

Explain evidence only when it is available for the analyzed resource version.
The present edge tuple deduplicates several references between the same two
resources, so an edge alone cannot prove a specific field path or `optional`
value. Missing source detail produces `unknown`, not an invented field path.
Do not fetch Kubernetes objects during analysis to fill such a gap. If an
internal store projection needs extending, preserve old public serialization
and the Secret sanitizer; review any persistent schema change separately.

## Result and query design

The fifth M2 slice implements this v1-only route in the unreleased development
tree. It is not available in the published v1.6.0 release:

```text
GET /api/v1/impact/{namespace}/{kind}/{name}
    ?cluster=prod&relation=dependents&max_depth=5&limit=200
```

`_` identifies cluster-scoped resources. In federation mode an explicit single
cluster is required; the first release does not infer cross-cluster impact.
In standalone mode omit `cluster`; the empty internal cluster identity selects
the local graph only. See the fifth slice for authorization and error behavior.

The response includes:

- A schema version, KubeAtlas version, generation time, and query scope.
- Root identity and UID when observed; reference-only status for Secret targets.
- Separate dependent/dependency results and authorization associations.
- A stable shortest representative path per result, hop count, original edge
  types/directions, evidence category, and any unknown/conditional facts.
- Returned resource counts, with workloads and Pods counted separately.
  Workload roll-ups must not add to the resource total a second time.
- Observation coverage and query-completeness fields described below.

Limits are explicit: default depth 5, maximum depth 10; default result limit
200, maximum 1,000; one representative path per result in the initial core.
The v1 handler enforces a 5-second processing budget and a combined 2 MiB
encoded response cap. Depth/result truncation yields `truncated=true`, a reason,
and lower-bound counts. Size-budget exhaustion returns 413 without a partial
success; deadline expiry attempts a 504 (an expired/disconnected socket may not
deliver it). Invalid bounds return 400, not silent clamping. Caller cancellation
propagates to the analysis and prevents any late result from being published.

Additional alternatives are a later enhancement, not a reason to enumerate all
simple paths. BFS order and tie-breaking use stable resource identity and edge
type order so equivalent input snapshots produce equivalent paths.

### Identity, consistency, and visibility

- Authorize the selected cluster before reading resources or calculating counts.
  Unauthorized/unknown roots must not leak hidden names, paths, or counts.
- Traverse within that cluster. Namespace/label display filters do not silently
  change the analysis scope. If a future query filter limits scope, return that
  scope explicitly; never label it a complete cluster result.
- Analyze one immutable resource/edge projection. Tier 2 must supply a bounded
  consistent read, not unrelated resource and edge queries at different database
  snapshots. Do not assume the existing `Snapshot` implementation already
  guarantees this; prove the adapter contract in M1 before exposing the API.
- Separate graph-consistency guarantees from collection freshness: a consistent
  local graph is still only an observation of Kubernetes, not a cluster-wide
  transactional snapshot.
- Reject an optional expected-UID precondition when an ordinary resource has
  been deleted and recreated. Never attach old-version evidence to the new UID.
- The existing identity uses cluster, namespace, kind, and name. Ambiguous CRDs
  from different API groups must not be claimed as disambiguated; built-in
  supported groups are the first scope, and unsupported ambiguity is explicit.
- Raw objects, bearer tokens, kubeconfigs, and Secret values are not added to the
  response. Non-Secret reference evidence is narrowly selected. Reports still
  contain sensitive topology and require a sharing warning, not an anonymity claim.

## Observation and completeness

Reuse the operational monitor, but do not equate API reachability with every
resource watcher being current. Record relevant resource-type coverage,
initial sync, watch/list failures, permission denials, processing/persistence
gaps, and the observation interval used for this result.

Two independent dimensions are required:

1. **Traversal completeness**: complete within the declared modeled graph and
   limits, or truncated with reasons.
2. **Observation coverage**: initialized and no known relevant gap, partial,
   stale, or unknown. This never promises that Kubernetes has not changed since
   the observation.

A quiet cluster is not stale because it emits no changes. Kubernetes does not
promise a fixed interval of watch bookmarks; the absence of a bookmark alone
must not cause a stale alarm. See the
[Kubernetes watch contract](https://kubernetes.io/docs/reference/using-api/api-concepts/#watch-bookmarks).

Only an initialized, non-truncated result with no known relevant observation
gap may say "No dependents found in the analyzed scope." Otherwise show
"No dependents observed; analysis is incomplete" with structured reasons.
Never say "safe to delete." History disabled or outside retention means
history unavailable, not zero past changes. Metadata-only history does not
support field-level diffs or reconstruction of deleted historical paths.

## Acceptance catalog

These are required future checks, **not currently passing feature tests**.
The M0 fixture tests below establish a smaller legacy baseline only. M1 adds
engine tests, M2 adds authorization/observation/API tests, and M3 adds client
and export tests against the same result contract.

| ID | Scenario | Required outcome | Stage |
| --- | --- | --- | --- |
| IA-01 | Required ConfigMap reference | Direct consumer and real edge evidence, not an outage prediction | M1 |
| IA-02 | ConfigMap environment reference | Describe restart-required refresh semantics, not immediate reload | M1 |
| IA-03 | Ordinary volume and subPath references | Keep their different update semantics and source evidence | M1 |
| IA-04 | Optional reference | Conditional relationship; no certain-failure wording | M1 |
| IA-05 | Edge without source-field evidence | Unknown field/optionality; no invented provenance | M1 |
| IA-06 | Pod/ReplicaSet/Deployment ownership | Correct directions; separate Pod/workload counts | M1 |
| IA-07 | Service selector and route chain | Declared path, not proof of traffic or live endpoints | M1 |
| IA-08 | PVC consumer | Trace declared mount; no durability claim | M1 |
| IA-09 | Secret reference | Reference-only target, unknown existence, no Secret API read/value | M1/M2 |
| IA-10 | Role-binding-subject branch | Separate bounded authorization association; no effective-permission claim | M1 |
| IA-11 | Multiple paths to one resource | Deduplicated count and deterministic shortest representative path | M1 |
| IA-12 | Cycle/self-loop | Termination, no repeated root/result, bounded path | M1 |
| IA-13 | Depth or result boundary | Explicit truncation and lower-bound counts | M1/M2 |
| IA-14 | Time/byte budget or cancellation | Stop bounded work; no false complete/empty success | M1/M2 |
| IA-15 | Cross-namespace reference in one cluster | Included unless the declared analysis scope excludes it | M1/M2 |
| IA-16 | Same names across clusters / denied token | No cross-cluster path or unauthorized count/name disclosure | M2 |
| IA-17 | Delete/recreate with a new UID | Expected-UID conflict; no old evidence attributed to replacement | M1/M2 |
| IA-18 | Missing target / wrong owner UID | Explicit unresolved evidence; no asserted matching instance | M1 |
| IA-19 | Unmodeled CRD/policy edge or OTel overlay | No unapproved propagation; explain coverage limitations | M1 |
| IA-20 | Initial sync / missing resource permission | Partial/initializing result, not complete zero impact | M2 |
| IA-21 | API responds but relevant watch fails | Coverage gap remains visible independently of reachability | M2 |
| IA-22 | Quiet healthy cluster / no bookmark | No event-age-only or bookmark-only stale classification | M2 |
| IA-23 | Store write failure / history unavailable | Report the corresponding gap; do not manufacture history | M2 |
| IA-24 | Store changes during query | One consistent graph projection or explicit bounded failure | M1 |
| IA-25 | Web filter/aggregation vs API/CLI | Same declared query yields same results; UI filter is not hidden scope | M3 |
| IA-26 | JSON/HTML export | Same captured result, no external HTML resources, topology warning | M3 |

## Verification and delivery

### Implementation progress

The first M1 slice adds the pure `analysis.AnalyzeImpact` core and portable Go
tests. It accepts a caller-owned immutable graph projection and implements:

- Deterministic breadth-first traversal for dependents/dependencies, with one
  representative shortest path, explicit stored/traversal directions, tuple
  deduplication, and cycle/self-loop handling. Ties follow each parent's
  neighbor resource ID, then edge type; output follows discovery order.
- Default depth 5 / result limit 200, hard maxima 10 / 1,000, and explicit
  truncation only when an additional eligible resource is omitted. Root counts
  are excluded. Controller-resource and Pod counts are disjoint; controllers
  include ReplicaSets and are not an application roll-up.
- Missing-endpoint and owner-UID mismatch notices, expected-root-UID checking,
  single-cluster selection, identity-only output, and reference-only Secrets.
  A known owner mismatch is not traversed; missing UID evidence stays unknown.
- Exclusion notices for authorization, unmodeled, runtime, and cross-cluster
  relationships in ordinary traversal. The fifth slice below adds a separate
  authorization facet. Notice counts describe encountered unique edge tuples,
  not a coverage inventory.
- Context cancellation during resource/edge indexing and traversal, without
  returning a successful partial or empty result. The core does not mutate its
  projection and supports concurrent readers of an immutable projection.

The tests include hand-authored path/count oracles, 100 input permutations of
a diamond/cycle graph, an independent shortest-distance relaxation oracle,
exact-limit and dense-graph cases, UID replacement, synthetic Secret canaries,
and cancellation checks. These exercise the pure core only, not all IA checks.

The second M1 slice adds the optional `graph.ImpactSnapshotter` capability and
`analysis.AnalyzeStoredImpact`. It does not change GraphStore v2's method set or
the legacy `Snapshot`/`SnapshotMetadata` methods. Both built-in stores now supply
an independently owned, single-cluster projection containing identity, owner
references, edge tuples, and the narrowly allowlisted consumer reference fields
described below. Full Raw objects, labels, source annotations, and arbitrary
edge attributes are excluded; Secret nodes keep only reference identity and the
generated reference-only marker. Same-cluster dangling tuples remain visible
as unresolved evidence, while cross-cluster tuples are excluded before analysis.

Tier 1 copies under one read lock, with a cancellable lock wait. Tier 2 reads the
resource and edge tables in one short, read-only repeatable-read transaction;
analysis runs after the transaction releases its connection. This avoids mixing
two database snapshots but does not make separate informer writes atomic. The
isolation semantics follow the
[PostgreSQL repeatable-read contract](https://www.postgresql.org/docs/16/transaction-iso.html#XACT-REPEATABLE-READ).
Existing resource cluster indexes support scoping; edge-prefix scans are time
bounded, not claimed to have an indexed scan-work bound.

Internal projection defaults are 20,000 resources, 100,000 edges, and a 16 MiB
conservative metadata/reference JSON-size budget. Hard maxima are 100,000 resources,
500,000 edges, and 64 MiB. The byte charge includes fixed field overhead and
worst-case string escape expansion; it is not a measurement of Go heap usage or
the final response size. PostgreSQL also guards individual projected rows before
transfer and reads at most each row limit plus one. Exceeding any budget returns
an error with no partial graph. The built-in reads and store-backed analysis use
a shared five-second context budget (or the caller's shorter deadline); database
rollback has a separate bounded one-second cleanup allowance. HTTP serialization
and the final end-to-end response deadline remain M2 work.

The shared Tier 1/Tier 2 contract tests pin projection contents, ownership,
cluster/namespace scope, unresolved references, paths/counts, exact limits,
cancellation, and reads after failures. PostgreSQL integration tests use a
disposable test container, including an actual atomic delete committed between
the two SELECTs and bounded single-connection pool reuse after cancellation.
No production database or Kubernetes credentials are needed. A visibility
wrapper without the explicit capability fails closed; there is no unrestricted
snapshot fallback.

The third M1 slice starts reference interpretation in the pure core. Its
`referenceEvidence` sidecar is keyed by the stored `from`, `to`, and `type` for
edges used in returned representative paths, not an inventory of all references
in the cluster. Interpretation never introduces an edge or changes traversal
counts. Multiple source fields and mounts for a deduplicated edge remain separate.

The interpreter accepts only the built-in Pod `v1`, Deployment/ReplicaSet/
StatefulSet/DaemonSet `apps/v1`, and Job/CronJob `batch/v1` source schemas. The
source object's kind, API version, name, namespace, UID, and resourceVersion
must match the analyzed consumer. `version_bound` means that the declaration is
bound to that consumer version, **not** that the referenced object's instance,
existence, contents, or current runtime use was verified. Missing or mismatched
source versions, unsupported schemas, malformed reference fields, and unmatched
edge targets produce explicit `unknown` evidence with no invented field paths.

Supported source fields include environment references in regular, init, and
ephemeral containers; ConfigMap/Secret volumes and projected sources; and PVC
declarations and volume mounts. JSON Pointer paths retain array indices. Each
use reports its own `required` or `optional` lookup behavior (PVC uses
`not_applicable`), rather than merging mixed references into one certainty claim.
Absence of `optional` means required only within valid, version-bound source
fields. Environment use reports `container_restart_required`, mounted
configuration volumes `eventual_projection`, and `subPath`/`subPathExpr`
configuration mounts `no_automatic_updates`. An unmounted volume declaration
does not justify a refresh claim. PVC refresh remains `storage_dependent`, even
for a subPath mount. These classify update mechanisms, not target mutability,
watch health, or application reload. In particular, immutable targets and
delete/recreate behavior are not resolved from the reference alone. See the
[ConfigMap documentation](https://kubernetes.io/docs/concepts/configuration/configmap/) and
[Secret documentation](https://kubernetes.io/docs/concepts/configuration/secret/#using-secrets-as-files-from-a-pod).

The interpreter reads only needed reference fields from the supplied non-Secret
source. It does not inspect literal environment values, command arguments,
configuration contents, or Secret payloads, and none appear in the sidecar.
Source field/mount paths are returned, not key contents, mountPath values, or
subPath expressions. Per-query scanning and evidence allocations have separate
100,000-work-entry / 16 MiB conservative budgets, including ignored entries in
supported arrays. Sources are interpreted once per query; cancellation or budget
exhaustion returns an error, never a successful partial result.

The fourth M1 slice connects reference fields to both built-in impact snapshots.
A shared, immutable field allowlist drives the Go copier and PostgreSQL SQL
compiler. Supported consumers carry only source identity/version, reference
names and optional flags, volume names, and mount-kind evidence. Array positions
remain intact, including empty placeholders for filtered entries. Nonempty
`subPath`/`subPathExpr` strings become a fixed presence marker; their values are
not copied. Invalid leaf/object/array types become explicit JSON null rather
than disappearing and accidentally activating a default. Secret and unsupported
resource schemas have no reference payload projection.

Within this new internal snapshot capability only, `Resource.Raw` holds the
**partial allowlisted reference projection**, not a complete Kubernetes object.
The legacy `Snapshot`/`SnapshotMetadata` implementations, public Resource
serialization (`Raw` remains excluded), and GraphStore v2 method set are
unchanged. The new analysis path never falls back to a full Raw read. Custom
providers that lack source fields continue to produce explicit `unknown`.

Tier 1 copies these fields within the same cancellable read lock as metadata
and edges. Tier 2 selects only the allowed fields in the existing read-only
repeatable-read transaction, before wire transfer; it does not fetch a full
container, environment list, or Raw object into Go and then redact it. The byte
budget includes the reference projection, with cancellation during nested Go
copying and a SQL-side per-row size guard. These budgets bound returned data,
not PostgreSQL's internal JSON evaluation memory or total scanned rows; database
work remains subject to the statement/context timeout. No schema migration,
persistent evidence column, or per-resource query is introduced.

Shared contract tests cover all seven supported consumer kinds against an
independent allowlisted fixture, pure/store-backed result parity, version/UID
mismatch, malformed/null evidence, nested ownership, and byte-budget failures.
PostgreSQL tests inspect the SQL result before Go decoding, including malformed
nested payloads, large ignored environment values, and oversized allowed names.
Local graph tests cover projection idempotence, immutable schema accessors,
legacy serialization, escape expansion, and cancellation during field copying.

Existing extractor coverage is unchanged: support in the interpreter does not
add missing edges.
Image-pull, Ingress, ServiceAccount, and Gateway Secret-reference details,
volumeDevices, and custom resource schemas are not interpreted by this slice.

The fifth M1 slice adds the independent `authorization` facet to the same pure
result. It reuses the single impact projection, without another store query or
reading role rules, binding payloads, tokens, or Kubernetes objects. Its finite
motifs depend on the query root, not on the ordinary traversal direction:

| Root | Supported association expansion |
| --- | --- |
| Role / ClusterRole | Bindings referring to that role, their ServiceAccount subjects, and workloads explicitly using those accounts |
| RoleBinding / ClusterRoleBinding | The referenced role and ServiceAccount subjects, then workloads using those accounts; the role is a leaf |
| ServiceAccount | Bindings naming that account and their roles; workloads using the account; no sibling subjects |
| Supported workload / Pod | Its explicit ServiceAccount edge, bindings naming that account, and their roles; no peer workloads or sibling subjects |

This is not an undirected connected-component search. It does not traverse
ownership or routing edges, infer group membership, expand aggregated role
rules, infer runtime credentials, or synthesize missing nodes. Supported
workloads are Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, and
CronJob. Known API-group/version or namespacing mismatches are excluded; absent
group/version metadata is not upgraded to verified source evidence. Other
roots are explicitly not applicable, not a complete empty RBAC assessment.

Steps retain original and traversal directions, plus the binding's scope.
A RoleBinding referencing a ClusterRole remains **namespace-scoped** to the
binding's namespace, even if its ServiceAccount subject is in another
namespace. A workload/account edge must remain within their shared namespace.
A ClusterRoleBinding can reference only a ClusterRole. These rules follow the
[Kubernetes RBAC binding contract](https://kubernetes.io/docs/reference/access-authn-authz/rbac/).

The facet reports `stored_edge_association`, `sourceVersionEvidence: unknown`,
`effectivePermissions: not_evaluated`, and `observationCoverage: unknown`.
Observed identity UID/resourceVersion does not bind an edge to that source
version. The association is therefore not a verified current grant, effective
permission calculation, outage prediction, or deletion recommendation.

Counts, paths, truncation, and notices are independent from ordinary results.
Each facet uses the query depth and result limits separately: the default can
return 200 ordinary resources plus 200 authorization resources, not 200
combined. The authorization facet has a separate conservative 16 MiB encoded
explanation budget and fails the entire analysis on exhaustion. The final
2 MiB HTTP cap remains an M2 requirement. Counts exclude the root, deduplicate
resources, and separate roles, bindings, accounts, controllers, and Pods. One
deterministic shortest representative path is retained per resource.

Encountered missing endpoints, invalid scopes, conflicting role/account
references, and unsupported User/Group subjects produce bounded reason/count
notices without their excluded endpoint names. They mark the facet as a lower
bound; they are not misreported as query truncation. A visited binding without
a role edge or workload without an account edge also remains incomplete.
Duplicate tuples do not create false cardinality conflicts. All checks concern
only the supplied motif: missing collection coverage or implicit group grants
cannot be ruled out by an otherwise complete stored traversal.

Only the new impact path's ID parsing and store projection were corrected to
recognize a cluster prefix before the first slash, preserving legitimate RBAC
names such as `system:node`. Legacy routes and store methods are unchanged.
PostgreSQL retains exact parameterized cluster selection, the same read-only
repeatable-read transaction, and the five-second budget; edge prefix filtering
does not establish an indexed scan or query-performance acceptance.

Portable tests cover all four root motifs, exact paths/counts, cross-namespace
subjects, namespace/cluster binding scope, absent/ambiguous references,
unsupported groups, output limits, payload exclusion, cancellation at every
context checkpoint, concurrent readers, and 100 input permutations. Shared
memory/PostgreSQL tests exercise standalone/federated `system:` identities,
literal `_`/`%` cluster names, cross-cluster filtering, mutation isolation,
independent limits, and dangling conflicts. A store probe forbids any extra
read beyond the one projection. These are local implementation tests, not
product API or effective-permission acceptance.

This M1 slice did not register a product endpoint. The fifth M2 slice below now
adds authorization before reads/counts, a deadline, and the combined response
cap. The pure core's indexing space is proportional
to its input; its depth/result bounds alone do not bound input reads. Cluster
selection is not an access-control check. Duplicate IDs present in the input
are rejected, but a collision already collapsed by a store cannot be
reconstructed here.

`modeledTraversalComplete` concerns only the supplied modeled graph, never
Kubernetes visibility or watch health. Observation coverage remains `unknown`
in this slice, including for a complete empty traversal. Known missing endpoints
or wrong owner UIDs mark the count as a lower bound without pretending that a
query limit caused them. No API route, Web/CLI behavior, or shipped version is
changed by this slice.

### M2 observation collection: first slice

The operational monitor now owns a separate, process-local `CoverageTracker`.
The core informer pipeline supplies per-cluster, per-group/version/resource
evidence in both standalone and federation wiring. Legacy probe-based metrics,
`/readyz`, existing routes, and GraphStore methods remain unchanged. The new
evidence is not yet attached to an impact API response; the pure M1 result still
reports observation coverage as `unknown`.

The recorder observes the existing list/watch requests and their lifecycle,
including permission-denied request/stream errors. It does not issue additional
requests or alter client-go's normal versus streaming-list selection, list
options, bookmarks, resource events, or retry policy. A forwarding watch holds
at most one in-flight event and exits on cancellation, upstream close, or Stop;
it has no extra buffered queue. Quiet watches need no event or bookmark heartbeat.

Initial delivery is tracked with **each handler registration's** `HasSynced`,
not only the informer's cache sync. A blocked initial graph write cannot be
mistaken for completed delivery; one forbidden resource type does not prevent
other types from reporting their own evidence. These semantics follow the
[client-go handler registration contract](https://pkg.go.dev/k8s.io/client-go/tools/cache#ResourceEventHandlerRegistration).

| State | Meaning within the requested resource-type scope |
| --- | --- |
| `unknown` | No registered evidence, or no resource types requested; not an empty complete observation |
| `initializing` | Initial handler delivery or watch establishment remains pending |
| `initialized_no_known_gap` | Initial delivery completed, watch established, and no recorded list/processing/persistence gap; not proof of an empty processing backlog or current Kubernetes state |
| `partial` | A known list/watch failure, stopped collector, processing gap, or mixed observed/unwatched requirement |
| `stale` | A known watch disconnection or stopped collector exceeded the monitor's configured stale interval; never derived from resource-event or bookmark age |

Malformed callback objects, extraction failures, and failed resource, reference
placeholder, edge, or delete writes record separate sticky gap reasons. One
successful update, resync, list/watch reconnection, or API/storage probe cannot
prove that all missing graph data was repaired, so it cannot erase these gaps.
They survive collector replacement and resource-type removal/re-addition within
the same process. No automatic repair or gap-reset operation is introduced.
Watch epochs and collector sessions reject callbacks from replaced streams or
collectors. This is observation, not a change to the pipeline's write/retry policy.

Snapshots contain only the selected cluster, requested resource-type identities,
timestamps, state, and a fixed reason vocabulary. No object names, namespaces,
UIDs, Raw objects, error messages, tokens, or kubeconfigs are retained in this
recorder. It does not register or read the core Secret GVR, including when a
caller explicitly includes that GVR in the informer's options. Secret targets
remain reference-only and are not evidence of Secret-object observation.

Recording is bounded to 128 cluster identities and 256 retained resource types
per cluster, not a map keyed by Kubernetes objects. Invalid or over-budget
registration fails explicitly; the informer does not claim successful coverage.
Snapshots sort/deduplicate their bounded requirements and return owned values.
The recorder is **not durable history**: process restart establishes a new
observation interval, not proof that older gaps or retained graph data were
reconciled. Removed cluster/type evidence is retained within those bounds.

Portable tests use fake clients and injected failures, including a blocked
initial callback with a concurrent denied GVR, exact forwarding of streaming
list options/events, closed/error streams, blocked-consumer cancellation,
stale-epoch/session rejection, scope isolation, registration bounds, sticky
data gaps, and absence of Secret or extra Kubernetes requests. These tests do
not establish real-cluster watch reliability, throughput, or endurance.

### M2 observation-aware analysis: second slice

`AnalyzeObservedImpact` brackets one consistent graph projection with two
context-aware recorder captures, then attaches separate ordinary/authorization
evidence to the internal result. Both captures, the projection, and analysis
share the existing five-second budget, including waiting for the recorder lock.
Invalid options and a store without the explicit projection capability fail
before either observation or graph reads. No extra Kubernetes, history, or
storage-probe request is made. The original pure and store-only M1 seams remain
unchanged and continue to report unknown observation coverage.

Required types are independent of successful query matches, depth, and result
limits. An empty graph cannot erase its own missing watch requirements:

- Ordinary analysis checks a fixed 26-type minimum from the current core and
  optional built-in collectors. Since `OWNS` accepts arbitrary resource kinds
  and independent CRD/Gatekeeper collectors also write the graph, this is **not
  a closed ordinary type scope**. The explicit
  `dynamic_source_scope_unverified` limitation prevents a complete-observation
  claim even when every minimum requirement is healthy. Unwatched optional
  APIs remain unknown; no stored objects does not prove an API is absent.
- The supported built-in RBAC association model checks 12 types independently:
  seven workload kinds, ServiceAccount, Role, ClusterRole, RoleBinding, and
  ClusterRoleBinding. Applicable roots and returned resources must have the
  exact supported API group/version; missing schema evidence remains partial.
  This scope is the modeled association facet, not effective permissions.
- Secret remains reference-only. Neither profile requests a Secret watch,
  and a Secret root does not become evidence of Secret-object observation.

Each capture includes a process-local collector generation and a per-type
continuity revision. List/watch transitions, initial delivery, stopped
collectors, and newly recorded processing/extraction/write gaps advance those
identities. A disconnect followed by recovery **inside the graph-read window**
is detected even when both endpoint states are healthy. Quiet watches and
ordinary successful object delivery do not by themselves advance the revision.
Unrelated cluster/type transitions do not contaminate the authorization facet.
These tokens are not durable restart/history identities.

An empty facet is `none_found_in_analyzed_scope` only when modeled traversal is
complete, the modeled type scope is closed, every required type reports
`initialized_no_known_gap`, and the capture window is stable. Otherwise it is
`none_observed_incomplete`; unsupported authorization roots are `not_applicable`.
The ordinary facet cannot reach the former assessment in this slice. None of
these labels means safe to delete, an outage prediction, effective permission,
zero processing backlog, or a transactional view of Kubernetes. Historical
edge/source-version limitations remain visible and are not overwritten by
healthy watch evidence.

Provider evidence must name exactly the selected cluster and requested types,
use the closed state/reason vocabulary, and be freshly captured during the read.
Invalid or failed evidence produces no partial success; raw provider error text
is never forwarded. Missing providers produce explicit unknown coverage.
Returned captures/facets own their data. Portable tests cover the requirement
oracle, empty results, recovered outages, replacement sessions, sticky gaps,
stale watches, schema uncertainty, isolation, invalid evidence, ownership,
read ordering, and cancellation before/during/after the graph read.

Remaining M2 work includes complete collector-to-API inventory qualification,
verified retained
history and snapshot-marker availability, and caller-visible explanations.
The seventh slice below adds standalone Gatekeeper coverage and targeted API
discovery evidence; the eighth adds bounded server API discovery and optional
API presence assessments. Neither establishes full graph observation.
Cached storage and recording status are described below; they are not retained
data verification. This internal wrapper is not a product endpoint and must not
be called with an unauthorized cluster scope. The fifth M2 slice below now
provides its additive v1 HTTP boundary, including authorization before recorder
reads, request cancellation, and the combined response cap.

### M2 storage and recording availability: third slice

`AnalyzeImpactWithEvidence` composes the same graph/coverage reads with cached
storage and history-writer evidence. This is an internal entry point, not a new
HTTP route or a database/history query. All work, including waiting for the
monitor lock, remains inside the original five-second analysis deadline.
Missing providers leave explicit unknown state and unknown storage durability;
invalid provider scope, timestamps, states, or reason codes return no partial
success and never expose provider error text.

Three facts remain separate:

- `currentGraphRead: succeeded` means this request obtained and analyzed its
  current projection. It does not overwrite an unavailable/stale background
  storage probe, or make historical data available.
- Storage status reports the last background check, its last success, durability
  configuration, and expiry interval. Both success and failure advance the
  check timestamp. An old successful check expires if the sampler stops;
  future-dated evidence is unknown. Reading this status runs no probe and reads
  no backup marker. Existing operational metrics and readiness are unchanged.
- History status distinguishes `disabled`, `unsupported`, `unknown`,
  `initializing`, `recording`, `degraded`, and `stopped`. `recording` means only
  that the metadata writer has started and its context has not ended. Queue
  loss and exhausted write retries remain explicit gaps after later success.
  Queued or in-flight delivery adds a pending hint; an empty queue alone cannot
  establish that all history is written. No event identities, payloads, or
  global event counters enter this result.

This third slice reports `content: metadata_only` and `retainedData: not_queried`.
Its coverage is unknown or known-partial, never complete merely because the
current process has zero error counters. The process observation start and
configured retention are not an earliest retained event, a verified historical
coverage window, a working snapshot marker, or a backup. In particular, an
outside-retention request must not be presented as zero past changes. Actual
retained-window/snapshot availability checks were pending in this slice; the
tenth slice below adds cached metadata observations. Disabled history
does not degrade an otherwise valid current graph/watch result.

Multi-cluster history is explicitly `unsupported` for this evidence source:
writer metrics are global, and older events can lack cluster attribution. The
fourth slice below fixes attribution for new informer events, but a selected
member must not inherit another member's loss counters, recording timestamp,
or a supposed complete history. The adapter rejects mismatched standalone/
federated query scopes and does not even sample the writer in the unsupported
case. This does not change existing history routes or backfill stored events.

The source-inventory limitation also remains explicit. Current optional-API
gating checks group versions, not exact resource-type absence; federation does
not yet start member-specific CRD/Gatekeeper collectors. Neither condition can
be converted into complete ordinary observation coverage.

Portable tests exercise cached-probe expiry and cancellation, feature/tier/
cluster modes, writer lifecycle and cancelled contexts, a blocked in-flight
write with an empty queue, real queue overflow and retry exhaustion, gaps after
successful drain, provider ownership/validation, and independence from current
graph counts. These are local tests, not real-cluster or retained-database
acceptance. The fifth M2 slice below supplies runtime API wiring, caller
authorization, and the combined response byte cap without changing these
availability meanings.

### M2 cluster-attributed history reads: fourth slice

New core informer add/update/delete events now carry the trusted manager's
`ClusterID`, including tombstones and retries after failed resource writes.
Annotations cannot choose the history scope. Unchanged resource-version resyncs
still produce no duplicate event. The existing asynchronous writer and both
stores preserve this metadata; no schema migration or history backfill is made.
Historical unlabelled rows cannot be reliably assigned to a member, and are
never relabelled from an object name, UID, current graph, or selected cluster.

`graph.HistoryEventReader` is a new **optional internal capability**, not a
GraphStore v2 method or an HTTP route. Both stores implement an exact single-
cluster, optional-namespace, inclusive time-window read. Empty cluster selects
only unlabelled rows, never all clusters; it cannot distinguish standalone
records from old unlabelled federation records. Results keep event ID, time,
cluster, resource identity, event type, and resource version. They never return
object `Data` or Secret-object events. Identical names and UIDs in other clusters
do not affect the page, its limits, or its truncation flag.

The default row limit is 200, the hard maximum 1,000. One extra matching row
sets `truncated: true`; exact-limit results do not. Rows are ordered by timestamp
then event ID, retaining separate versions and events rather than merging by UID.
The metadata budget defaults to 1 MiB and has a 2 MiB maximum, using a conservative
escaping-aware size estimate. Exceeding that budget or any cancellation returns
an error with no partial page. This is not the eventual combined HTTP cap.

Memory reads hold one cancellable read lock over the bounded event ring.
PostgreSQL applies exact parameterized scope and row limits in SQL, with a
metadata-only projection and a per-row wire-size guard before Go decoding. The
single SELECT runs in a read-only transaction with a local statement timeout;
pool acquisition and reads share a five-second context budget. Cancellation
rolls back/releases the connection, with a separate one-second cleanup deadline;
this optional history reader is not invoked by the new impact HTTP handler.
Existing time/namespace indexes are retained;
there is no new cluster-index migration, and row limits do not prove a bounded
number of scanned rows or production query-performance acceptance.

Authorization must precede this capability. A legacy visibility wrapper does
not gain it by embedding GraphStore; callers must not unwrap the wrapper or fall
back to global `ListEvents`. Existing snapshot routes and legacy `DiffWindow`
remain unchanged: their reads are not selected-cluster evidence, and the legacy
diff groups by UID/name without a cluster key. They are not reused by the new
impact path. This slice does **not** establish caller visibility for those routes;
member-safe product history requires explicit authorization and a separate API
integration review. No new event page is exposed over HTTP here.

A successful page describes only matching **retained** events. An empty page
does not prove no changes, and `truncated: false` does not prove collection or
retention completeness. The memory ring may already have evicted rows, retention
may have pruned them, and the writer may have gaps. Snapshot-marker verification,
per-member writer lifecycle/gaps, and retained-window qualification remain
pending at this stage. The tenth slice adds scoped retained-metadata checks.
Without that cache, `AnalyzeImpactWithEvidence` reports `retainedData: not_queried`,
and federation recording evidence remains `unsupported`; this fourth slice does not
silently promote either status based on a successful event read.

Portable producer tests cover concurrent same-identity clusters, standalone
scope, tombstones, failed-write retry, and real fake-client informer callbacks.
The shared store contract covers exact scope (including SQL wildcard characters),
namespace/time filtering before limits, inclusive boundaries, stable ordering,
independent ownership, payload/Secret exclusion, lookahead truncation, metadata
budgets, and cancellation. Disposable PostgreSQL tests additionally use a role
without access to the payload column, enforce the wire guard, and check cancelled
query cleanup through bounded reuse of a one-slot pool. These are local tests,
not acceptance against an existing cluster or production database.

### M2 authorized v1 impact API: fifth slice

`GET /api/v1/impact/{namespace}/{kind}/{name}` now wraps the internal analysis
and evidence providers. It is registered and documented only in v1; the frozen
v1alpha1 route/spec/component shapes are unchanged. The executable supplies its
existing coverage tracker and cached storage/writer availability reader in both
standalone and federation modes. No new Kubernetes request, probe, backup read,
history query, or mutating operation is issued by an impact request.

Federation requires exactly one explicit attached cluster. The existing cluster
visibility provider is consulted before attachment/existence checks and before
any graph, coverage, or availability read. Denied requests return fixed 401/403
messages without names/counts; an authorized but absent cluster/root returns a
fixed 404. A configured allow-set does not silently authorize the empty standalone
identity. With no visibility rules, the existing all-visible policy still applies:
this is not new built-in authentication, and external access protection is needed.

Parameters are `cluster`, `relation`, `max_depth`, `limit`, and `expected_uid`.
Duplicate, unknown, empty, malformed, or out-of-range values return 400 before
data reads. Explicit zero is not a default. `_` denotes a cluster-scoped root;
RBAC names such as `system:node` remain valid. Root namespace is not a traversal
filter. Expected-UID mismatch or absence returns 409, including a Secret reference
whose UID cannot be verified. No wrapper is unwrapped for an optional capability;
an unsupported store returns 503 rather than falling back to global reads.

The versioned envelope carries `schemaVersion: "1"`, build version, generation
time, selected cluster/mode/namespace scope, `analysis`, and a topology-sharing
warning. It contains the ordinary and authorization facets, path/reference
explanations, before/after observation, and separate storage/recording evidence.
Responses are `Cache-Control: no-store` and vary on Authorization. Closed error
messages never forward provider, database, or panic text. No field values from
an arbitrary resource payload enter the response.

The five-second deadline starts at handler entry and covers parsing,
authorization, evidence/graph reads, analysis, and encoding. Socket writes use
the same deadline through the existing middleware. A slow or disconnected
client is not guaranteed delivery, including delivery of the timeout error.
At most two impact requests per server are admitted; additional requests receive
429. A timed-out worker retains its slot until it actually stops, and a successful
worker retains admission until response writing finishes. This bounds even a
faulty provider that ignores cancellation; it does not make such a provider safe
or forcibly terminate its code. Late results never touch the response writer.

Before whole-response JSON allocation, a cancellable, exact-size traversal of
the closed DTO checks one combined 2 MiB cap, including both facets, explanations,
coverage, and the envelope. It handles embedded fields, nils, `omitempty`, Unicode,
and JSON escaping; unsupported arbitrary maps/interfaces fail closed. Exact-cap
responses pass, one extra byte fails. A size failure returns 413 without
success-shaped partial results or trimmed paths/counts. Missing evidence stays
unknown; ordinary coverage still carries `dynamic_source_scope_unverified`, and
history remains `retainedData: not_queried`. Federation recording evidence is
still unsupported until member-scoped writer evidence is implemented.

Local HTTP tests cover denied reads, exact cluster/cross-namespace scope, query
validation, UID and Secret boundaries, facet truncation, provider failure/panics,
timeouts, admission ownership, response-size boundaries, OpenAPI isolation, and
write-deadline forwarding. These are not real-cluster authorization, performance,
or release acceptance. Existing snapshot/diff API visibility and retained-window
qualification remain separate follow-up work; this handler does not repair or
reuse those legacy history paths. Client integration follows in M3 below.

### M2 CRD-source coverage: sixth slice

The standalone CRD collector now records its own source evidence, independently
of the core informer session. The existing CRD list/watch and each selected
per-CRD list/watch are observed without additional Kubernetes requests. Initial
delivery is complete only after that handler registration has finished its
initial callbacks, not merely after the informer cache syncs. Conversion,
Rego evaluation, resource/edge persistence, and delete failures leave explicit
processing/extraction/persistence gaps. Successful resync or re-registration
does not erase a previously recorded data gap.

Each registration receives a separate callback token. Stopped or replaced
callbacks cannot alter its successor's coverage, including late watch closure.
A per-CRD re-registration uses a fresh informer: a shared factory can retain a
stopped informer that cannot run again. CRD tombstones now stop their registrations
as ordinary deletion events do. Existing graph data is still retained after
CRD deletion; this slice does not assert that relisting repairs missed deletes
or reconstructs historical edges.

The tracker retains at most 128 distinct clusters across core and dynamic
sources. Source names are closed (`crd`, `gatekeeper`), with at most 256 retained
resource-type records per source/cluster, including stopped types. Core records
keep their existing separate 256-type bound. Exhausting a dynamic evidence cap
sets sticky `inventoryLimited` and **does not disable collection** or silently
evict earlier gaps. Snapshots own their bounded data and include no resource
object names, payloads, Secret values, or raw error strings.

The impact API carries these records in
`analysis.observation.ordinary.before.sources` and `.after.sources`. Source
cluster identity, generation, type/reason vocabulary, bounds, timestamps, and
ownership are validated before disclosure. The records share the existing
authorization boundary, five-second deadline, and combined 2 MiB response cap.
A source registration, stop, reconnect, or gap between captures makes the
ordinary window unstable; a missing source is explicitly unavailable. Unrelated
dynamic-source failures do not contaminate the closed built-in authorization
profile. A source's healthy registered types are never a complete API inventory.

Gatekeeper coverage was not wired in this sixth slice; the seventh slice below
adds its separate pipeline evidence. Generic CRD observation of Gatekeeper
objects cannot substitute for that pipeline. Federation still does not run
per-member CRD/Gatekeeper discovery and keeps unavailable source evidence,
rather than substituting standalone or core signals. Optional API absence and
CRD registration replacement were not qualified in this sixth slice; the
eighth and ninth slices below add those scoped behaviors. Complete collector
coverage and version equivalence remain unverified. Therefore `typeScopeClosed` stays false,
`dynamic_source_scope_unverified` remains, and an empty ordinary result remains
`none_observed_incomplete`.

Portable fake-client tests exercise real informer callbacks, blocked initial
delivery, failed initial writes, metadata/child list and watch denials,
tombstones, fresh re-registration, capacity exhaustion, and callback isolation.
Recorder/analysis/API tests cover concurrent lifecycle changes, cross-cluster
isolation, source-window changes, rejected provider evidence, and response
ownership. These are local functional/race tests, not real-cluster reliability,
performance, complete M2 acceptance, or release evidence.

### M2 Gatekeeper-source coverage: seventh slice

Standalone Gatekeeper discovery now contributes its own `gatekeeper` source,
separate from `crd` even when both read the same objects. The existing
ConstraintTemplate and generated Constraint list/watch requests supply the
evidence; no query-time Kubernetes request is added. Handler-registration sync
qualifies initial delivery. Decode, extraction, resource/edge persistence, and
delete failures remain explicit, sticky gaps. Discovery success does not clear
those gaps or establish permission to list/watch.

The existing startup/wait-loop discovery now checks the exact supported
`templates.gatekeeper.sh/v1/constrainttemplates` resource, including kind,
cluster scope, and advertised list/watch verbs. Group existence alone is
insufficient. Its optional `discovery` record contains only `state`, `checkedAt`,
`revision`, and `stale`, using these closed states:

| State | What the check actually establishes |
| --- | --- |
| `unknown` | No successful check is claimed; without a discovery client, legacy unconditional observation is preserved with a zero check time. |
| `advertised` | The response advertises the supported template resource shape, not controller health, watch permission, or complete Constraint inventory. |
| `not_advertised` | That exact resource was absent from the response, or discovery returned 404; **not proof that Gatekeeper is uninstalled**. |
| `permission_denied` | Discovery returned 401/403; absence is unknown. |
| `failed` | Discovery failed, timed out, or returned an invalid/unsupported response shape; absence is unknown. |

The production discovery request is context-bound with a five-second limit.
Legacy injected clients lacking contextual discovery retain their synchronous
method, without spawning an abandoned cancellation goroutine. While the API is
unavailable, the existing five-minute polling continues without starting its
list/watch. Once advertised, the collector starts and that discovery polling
stops. The cached check becomes `stale` after ten minutes; this degrades source
qualification, **not the health of an otherwise connected watch**. There is no
new background refresh or hot-path probe. API discovery is historical evidence,
not a permanent installation assertion.

The dynamic informer manager now uses a fresh informer for each registration,
including remove/add. Handler callbacks and list/watch share the same child
context and coverage token. Duplicate registration does not reset evidence;
removed or replaced callbacks cannot change successor coverage. Shutdown rejects
new registrations. Template tombstones stop their children, with existing graph
data retained. Core Secret registrations and invalid template kinds are rejected;
the existing evidence cap still does not disable collection.

The v1 impact response includes and validates the new discovery record under
the existing authorized cluster scope, five-second deadline, and combined 2 MiB
cap. Invalid vocabulary, scope, revision, time, stale flag, or false healthy
qualification fails closed. Discovery changes between graph-read captures make
the ordinary window unstable; unrelated built-in authorization coverage is
unchanged. Response records do not alias recorder/provider data.

Portable tests cover exact-resource discovery, missing clients, absence,
401/403 and failures, cancellation, cache aging, scope isolation, real fake-client
informer callbacks, list/watch denials, blocked/failed initial processing,
tombstones, remove/add, sticky extraction/write/delete gaps, stale callbacks,
shutdown, and evidence-cap exhaustion. They do not contact a real cluster or
read any existing Secret value. At this seventh slice, per-member dynamic
collectors, complete API inventory, optional-API absence qualification, and
generated Constraint version/scope negotiation remained unverified. The eighth
slice below adds server inventory and scoped optional API assessments.
`typeScopeClosed` remains false,
`dynamic_source_scope_unverified` remains, and an empty ordinary result stays
`none_observed_incomplete`. This is local functional evidence, not complete M2
acceptance or a production reliability claim.

### M2 API inventory and optional API presence: eighth slice

The standalone core informer now owns a separate background API inventory
worker. It checks core discovery and all advertised group versions at startup
and every five minutes. Each round shares a ten-second deadline and queries
group-version resources sequentially, with at most 64 group versions and 256
top-level resource descriptors. It records group, version, resource, kind,
namespace scope, and advertised list/watch verbs. Subresources are excluded.
These requests fetch API metadata, not objects; discovery of a Secret endpoint
does not read Secret objects or values. Inventory work never enters graph
handlers or the impact request path, and it does not delay informer readiness.

`ordinary.before.apiInventory` and `.after.apiInventory` include a separate
collector generation, revision, check time, state, stale/stopped flags, the
limit flag, and owned descriptors. The state is one of `unknown`, `complete`,
`partial`, `permission_denied`, or `failed`. `complete` means all resources
from the advertised discovery scope were successfully enumerated within this
round's bounds. It does not mean they were observed by graph collectors, or
that discovery is a Kubernetes transaction. A missing group-version response,
malformed or duplicate descriptor, or aggregated discovery failure prevents
completeness. Core/v1's client-go 404-to-empty fallback cannot qualify a complete
inventory. Failed or denied rounds replace old success evidence; raw response
errors never enter the record. Evidence ages after ten minutes, and shutdown
marks the worker stopped. Replaced worker callbacks cannot update successors.
The shared 128-cluster cap includes inventories, core, and dynamic sources.

The ordinary facet adds seven `optionalApis` assessments, covering Gateway,
HTTPRoute, Kyverno Policy/ClusterPolicy, the two policy reports, and OpenShift
Route. Each reports presence before and after the graph read:

| Presence | Evidence required |
| --- | --- |
| `advertised` | A fresh, complete, running inventory contains the supported resource version, kind, scope, and list/watch verbs. |
| `not_advertised` | That inventory has no matching group/resource in any advertised version. This describes the discovery scope, not operator installation. |
| `other_version_advertised` | The resource exists in another advertised version; the supported version is not assumed equivalent. |
| `unsupported_shape` | The supported resource version has a different kind/scope or lacks advertised list/watch capability. |
| `unknown` | Inventory is unavailable, incomplete, denied, failed, stale, stopped, or limited. |

Startup filtering now checks each exact requested resource's list/watch verbs
and queries each group version once, rather than substituting group existence
for resource availability. Missing optional resources are skipped; missing
required resources or discovery errors fail startup. This remains a startup
selection, not automatic registration of newly installed optional APIs.

`unobservedApis` exposes advertised list/watch endpoints without an active
registration evidenced by the captured core minimum or dynamic source rows.
It identifies missing observation evidence, not a proof that no external
collector exists. Core Secret endpoints are explicitly excluded from this list.
Different served versions are not merged without version-equivalence evidence.
Watch, handler, and write failures still retain their own coverage gaps; API
discovery cannot clear them. Changes to inventory generation, revision, state,
or descriptors across the graph read invalidate the ordinary window's
stability. The built-in authorization facet keeps its independent requirements.

Provider inventory identity, times, vocabulary, shape, bounds, uniqueness, and
ownership are validated before graph reads and disclosure. The response uses
the existing authorized cluster scope, five-second request budget, and combined
2 MiB cap. The production wiring currently supplies standalone inventory only;
federated members keep unavailable inventory instead of using the default
cluster's APIs. Injected clients lacking contextual discovery stay unknown.

Portable tests exercise the real client-go HTTP discovery path, cancellation,
aggregated failures, the core/v1 404 fallback, malformed metadata, all served
versions, resource/group-version caps, exact startup filtering, informer-worker
lifetime, scope isolation, recorder ownership, and invalid provider rejection.
The static watch requirement rows remain visible alongside presence assessments;
absence does not rewrite a watch as healthy. Complete collector coverage,
generated CRD version equivalence, per-member discovery, and retained history
qualification remain pending. Ordinary `typeScopeClosed` stays false and empty
results remain `none_observed_incomplete`.

### M2 CRD registration reconciliation: ninth slice

Standalone CRD registrations now belong to the CRD's name and UID, rather than
only its selected group/version/resource. The metadata informer selects the
served storage version, or the first served version when no served storage
version is available. A change in selected endpoint, kind, namespace/cluster
scope, or CRD incarnation cancels the old registration before starting a fresh
informer and coverage token. Unchanged schema/status updates and resync delivery
do not reset observation. Both namespace-scoped and cluster-scoped CRDs remain
supported. Defensive shape-change tests do not imply Kubernetes permits live
mutation of normally immutable CRD fields.

A CRD with no served version retires its stored selection; it does not leave
an obsolete informer healthy while waiting for a later update. Invalid
replacement identities also stop their named registration and leave a metadata
processing gap, without issuing a request for the invalid endpoint. Core
resources, Secret kinds, subresources, missing UIDs, malformed endpoints, and
unknown scopes cannot create a CRD registration. Deletion uses the stored
selection for the matching name/UID, even if the tombstone's version selection
is outdated or unserved. An old UID cannot stop a recreated CRD. Both value
and pointer tombstones are supported.

Callbacks for the same CRD serialize across overlapping informer lifetimes,
including delete/recreate. Cancelling a context cannot undo a store operation
already in progress when that store ignores context. Successor graph delivery
therefore waits for the old delivery to finish; cancelled callbacks cannot
proceed to another extraction/edge-write stage. The metadata handler does not
wait for a blocked graph callback, and unrelated CRDs have separate delivery
locks. A stuck store operation can still delay that CRD's successor, which
must remain initializing rather than claim completed initial delivery. Lock
ownership is retained until the actual informer handler processor exits and
released after its last overlapping lifetime, not accumulated for every
historical CRD name.

Retired-version evidence and sticky processing/extraction/persistence gaps
remain visible. Same-endpoint replacement advances continuity; stopped tokens
cannot alter successor coverage. Graph nodes and edges are still retained when
a CRD registration stops. This is not missed-delete repair, historical graph
reconstruction, or proof that two served versions are equivalent. The impact
query remains read-only and consumes cached source evidence; no cluster write
or analysis-time discovery request was added.

Portable fake-client/race tests cover storage-version changes, served-version
fallback, kind/scope/UID replacement, unserved transitions, unchanged resync,
UID-bound deletion, invalid shapes, stale-token isolation, and blocked writes,
deletes, and extraction crossing a replacement. They use the context-ignoring
memory backend to avoid overstating cancellation. Real-cluster replacement
acceptance, Gatekeeper-generated Constraint version/scope negotiation,
per-member discovery, complete collector coverage, version equivalence, and
retained history qualification remain pending. Ordinary `typeScopeClosed`
stays false and empty results remain `none_observed_incomplete`.

### M2 retained history metadata: tenth slice

The standalone Tier 2 profile with history enabled now samples retained event
and snapshot-marker metadata at startup and every five minutes. Each read has
a five-second context budget. Impact requests load the fixed-size cache; they
do not issue event/marker queries or refresh expired checks. Disabled history,
Tier 1, and federation do not start this worker. An observer owns one configured
cluster and retention duration, starts once, and marks its cache stopped when
its context ends.

The optional `graph.HistoryBoundsReader` capability is implemented by both
stores without adding GraphStore v2 methods. It returns the first and last
retained non-Secret event timestamps and the latest snapshot-marker timestamp
inside an inclusive, exact-cluster window. Empty cluster selects only unlabelled
records, whose legacy provenance remains unknown. No namespace/object identity,
payload, resource/edge count, or backup content is read or returned. There is no
fallback through global `ListEvents` or `ListSnapshotMeta`; wrappers must
explicitly implement the capability. A marker is a stored metadata record,
not a verified restore point. The existing trigger records counts and does not
establish a historical graph, drained writer, lossless observation, or backup.

Memory reads use one cancellable read lock over the bounded event and marker
rings. PostgreSQL uses one read-only, deadline-bound SQL statement so both
aggregates share a statement snapshot. Exact cluster, time, and Secret filters
apply before results leave the database. The lower time bound rounds inward
to PostgreSQL's microsecond precision, preventing inclusion of an adjacent
out-of-window record. Pool waits, query cancellation, transaction cleanup, and
single-slot reuse are tested. Output is constant-sized; scanned database work
is bounded by time, not a promised row count or performance benchmark. This
slice adds no database migration or index.

The v1 impact response adds `availability.evidence.history.retentionEvidence`:

| State | Meaning |
| --- | --- |
| `not_queried` | This observer has not completed a retained-metadata check. |
| `observed` | A check successfully read the exact window in `bounds`; zero event/marker timestamps mean no matching retained records in that window. |
| `failed` | The attempted check failed, was cancelled, or returned invalid evidence; old success records are cleared. |
| `unsupported` | The store/wrapper lacks the optional scoped capability; no query was made. |

The record also includes `checkedAt`, `stale`, `stopped`, and `bounds` with
`clusterId`, `from`, `to`, `firstEventAt`, `lastEventAt`, and `latestMarkerAt`.
Evidence expires after ten minutes. The parent `retainedData` is `observed` for
a successful cached observation, `unavailable` for a failed/unsupported check,
and `not_queried` before the first check or without a configured observer.
Consumers must inspect freshness and shutdown flags even when `retainedData`
is `observed`; the recorded window does not slide forward at request time.

Observed bounds do not prove a continuous retained interval. Empty bounds do
not prove no past changes; an old record may have been pruned or ring-evicted,
and a missing marker may be outside this window. Nothing outside the checked
window is qualified. Writer gaps, pending delivery, storage health, current
graph success, and unknown/partial history coverage retain their independent
meanings. No successful metadata query upgrades history coverage to complete.

Availability validation rejects mismatched cluster/window/retention, future or
inconsistent timestamps, stale claims, unknown states, and failed checks that
carry old success data. Provider-owned records are copied before returning.
Authorization still precedes all evidence access, and the response retains its
five-second total deadline and 2 MiB cap. Named members cannot inherit this
standalone cache; member-specific writer/retention integration and visibility
of the existing legacy history routes remain pending.

Portable tests cover cache-only request reads, blocked queries, cancellation,
shutdown, expiry, failed/invalid evidence, writer-gap independence, and API
disclosure. Shared memory/PostgreSQL contracts cover exact scopes (including
SQL wildcard characters), time boundaries, empty and marker-only windows,
Secret exclusion, pruning, and returned ownership. Disposable PostgreSQL tests
run the aggregate under a role allowed to read only timestamps, scope, and kind,
with no payload/identity/count access. These checks establish retained-metadata
behavior, not real-cluster continuity, recovery, performance, or release acceptance.

### M3 server-backed Web analysis: first slice

The standalone resource-detail page now offers an explicit **Analyze impact**
action. Topology resource nodes open the same response panel; aggregate nodes
cannot start an analysis, even when their IDs resemble resource IDs. The default
relation is `dependents` at depth 5. Both surfaces expose `dependencies` and
depths 1/2/3/5/10, with a fixed per-facet result limit of 200. Neither unbounded
depth nor the ambiguous legacy `both` direction is submitted to the API.

Counts, direct/indirect matches, representative shortest paths, stored-edge
directions, reference field evidence, independent authorization associations,
observation limits, and cached history status come from one v1 response.
The topology canvas highlights only returned ordinary resource IDs and exact
path edge tuples already present in the displayed view. An omitted or aggregated
canvas node does not reduce the server count; graph filters are not query scope.
The legacy local BFS remains only as a reference-test helper, not a product
analysis fallback. Runtime edges are not inferred into returned path highlights.

Requests include an expected UID when the standalone detail response provides
one. Resource views without a captured UID cannot pin a previous instance.
Federated topology requests keep their exact cluster prefix; resource detail
does not strip that prefix and fetch an unrelated standalone object. Cluster
selection changes close the old analysis. Query keys include identity, expected
UID, relation, and depth; abandoned reads consume cancellation signals, and
late responses cannot replace another query's result. Returned schema, root,
cluster, UID (when pinned), relation, depth, and limit must match the request.

These are captured analyses, not live subscriptions: explicit refresh replaces
the result, with no polling, automatic error retry, focus/reconnect refetch, or
persisted browser analysis cache. Loading and errors conceal previous successful
counts. Missing APIs, access denial, UID conflicts, limits, capacity exhaustion,
and unavailable evidence never become a local estimate. Error UI uses closed
messages rather than arbitrary upstream response text.

Incomplete empty results say **No dependents observed; analysis incomplete**
(or dependencies/associations). Complete absence wording additionally requires
the server's closed, stable observation assessment and completed traversal.
Retained event bounds and marker records remain separate from writer coverage,
continuous history, and verified recovery points. Responses render as text and
retain the sensitive-topology sharing warning. Web download controls remain
pending; the CLI export slice below does not yet export the Web's open capture.

Portable component/hook tests cover wire identity, deduplication, cancellation,
late cross-cluster responses, closed errors, root selection, cluster switching,
direction/depth changes, truncation, empty results, separate authorization,
retention qualification, and text escaping. Browser checks use synthetic data,
not an existing cluster or a release candidate. Cross-surface parity, richer API
inventory detail presentation, and real-server Web end-to-end acceptance remain
pending; this is the first M3 slice, not completion of the M3 gate.

### M3 CLI and captured reports: second slice

`kubectl atlas impact <Kind> <name>` now reads one server-backed v1 response and
renders text, JSON, or self-contained HTML. The Go API envelope and CLI use the
same DTO; the command never rebuilds relationships from a graph or uses a legacy
fallback. The existing view/diagnose commands retain their behavior. See the
[CLI reference](../cli-reference#kubectl-atlas-impact-v17-unreleased) for flags.

Queries preserve exact member cluster, namespace/root, relation, depth, limit,
and optional expected UID. Responses must match those values, the schema,
scope mode, and captured identities/paths. Unknown payload fields, missing
observation/availability, inconsistent paths, and foreign-cluster matches fail
closed. HTTP reads have a 2 MiB cap and a deadline, and never follow redirects
or retry automatically. Bearer credentials come only from `KUBEATLAS_TOKEN`;
remote URLs require HTTPS, and errors do not echo upstream payloads or URLs.

The HTML and JSON renderers consume one unchanged capture. The HTML embeds the
complete JSON as escaped text, with no scripts or external resources and an
explicit content policy. Both report formats retain the topology warning.
Terminal text neutralizes control/format characters. Ordinary paths/counts,
reference declarations, separate authorization associations, observation gaps,
and cached storage/history evidence stay distinct; an incomplete empty result
never becomes a safety assertion. Stale/stopped retention evidence and zero
timestamps remain qualified, not recoverability claims.

`--out` exclusively creates a private file and rejects existing paths/symlinks;
failed HTTP/schema/identity reads emit no report. Successful captures, including
partial or truncated analyses, exit 0; operational errors exit nonzero. A
partial file after an I/O error remains for explicit inspection, not silent
deletion. Separate invocations capture separately; rendering itself never reads.

Portable tests exercise the actual API handler, store, engine, HTTP client,
and report renderers for standalone/federated and both traversal directions.
The API JSON, CLI JSON, and HTML's embedded JSON match, including cross-namespace
results and incomplete empty results. Command tests check pure stdout, temporary
tunnel cleanup, private output permissions, non-overwrite, and failure behavior.
These are local synthetic integration checks, not real-cluster acceptance.
The third slice below adds Web export controls. The full real-server Web/CLI
end-to-end gate remains pending.

### M3 Web captured exports: third slice

Resource-detail and topology analysis panels now offer **Download JSON** and
**Download HTML** for their currently displayed server response. First select
**I understand this file contains sensitive cluster topology.** That consent
belongs to one immutable capture, not the resource name or query: an explicit
refresh or a changed selection requires a new acknowledgement. Loading and
error states hide the export controls. A download is a direct user action,
with no new analysis request, upload, or browser-storage copy. The browser
controls the destination and may cancel or block the download; the UI reports
only that a download was requested, not that the file was saved.

The impact-only HTTP reader applies a 15-second deadline and a decoded 2 MiB
limit, requires valid UTF-8 JSON, and rejects redirects, retries, unknown
fields, duplicate object keys (including escaped duplicates), incomplete
evidence, mismatched scopes/bounds, inconsistent counts, and invalid paths.
It validates the complete wire object against `web/src/api/impactSchema.json`,
not the smaller display-only TypeScript interface. The schema is a checked-in
copy of the expanded Go `ImpactResponse` OpenAPI schema;
`TestImpactWebSchemaMatchesAPI` prevents DTO/schema drift. Schema changes must
update that file and its behavior tests together. Legacy HTTP clients are
unchanged.

The reader retains the original JSON text with the immutable result, so JSON
downloads and the HTML report's complete-capture block preserve all supported
evidence, nanosecond timestamps, and integer text beyond JavaScript's safe
integer range. The HTML summary is a presentation, not a replacement for
that original JSON. It uses escaped text, an explicit content policy, and no
scripts or external resources. Ordinary paths/counts, reference declarations,
separate authorization associations, and coverage/history limitations remain
distinct. Partial or truncated results can be exported with their limitations;
an incomplete empty result is never a safety claim.

Portable tests cover the reader, malicious/unknown payload rejection, raw-text
fidelity, per-capture consent, selection/loading/error states, download failure,
and object-URL cleanup.
Synthetic client checks do not establish deployed collector coverage or
release acceptance.

### M3 cross-surface contract matrix: fourth slice

Six shared synthetic scenarios now connect the actual Go API, CLI command,
Web response reader/panel, and JSON/HTML exporters. The graph and independent
identity/count expectations live in `test/fixtures/impact-analysis/client-matrix.json`;
`client-captures.json` freezes the complete API responses, not a display-only
subset. API tests seed the real memory store and run the handler/engine before
comparing those captures. CLI and Web tests consume the same captures, check
query identity, displayed counts and paths, and preserve the complete export.
Only server version and capture clocks are normalized in the API comparison.

| Scenario | Expected ordinary result | Distinct qualification |
|---|---|---|
| ConfigMap dependents | 2: Pod direct, Service indirect, across namespaces | Stored edge direction differs from traversal direction |
| Service dependencies | 3: Pod direct, ConfigMap and Secret indirect | Secret is reference-only, not an observed object or value |
| Depth 1 | At least 1: Pod direct | `max_depth` truncation and lower-bound count |
| ConfigMap dependencies | 0 | Observation unknown; incomplete empty result, not safety |
| Role authorization | 0 ordinary; 3 separate authorization associations | Binding namespace retained; not effective permissions |
| Federated east member | 2, both scoped to east | Same names in west cannot enter the result |

Shared capture checks compare downloaded JSON byte for byte with its HTTP
response and require HTML to embed that exact text. Web and CLI captures may
have different capture clocks; their other evidence must agree. Topology
nodes have no previously captured UID to pin, unlike standalone detail nodes;
fixture checks must still match the seeded instance and report identity.

Federated synthetic tests isolate the allowed east member. Canvas filtering
and report downloads must not add analysis requests; switching to all clusters
must clear the previous result and exports. A denied west query must return
HTTP 403 and create no CLI report. Synthetic trusted-proxy tests do not
establish an operator's authentication deployment; observation coverage in
this matrix intentionally remains unknown.

Synthetic client checks do not establish real-cluster watch reliability,
performance, production authorization configuration, or release acceptance.
Keep temporary servers, downloads, screenshots, and run-specific evidence
outside the repository; portable fixtures and tests belong in source.

### M3 responsive analysis panels: fifth slice

Impact mode, depth, and relation controls now live in the detail panel with
their captured result, not over the canvas filters. Below the medium viewport
breakpoint the panel overlays the canvas without squeezing it; the cluster
strip remains available. Covered canvas controls are inert, focus moves into
the panel on entry/mode changes, and closing it restores the previous focus.
**Back to resource** and Escape exit analysis; the resource detail can then
be closed. Refresh does not repeatedly move keyboard focus.
Selecting the same graph node again after closing its detail panel reopens it.

Narrow topology filters are explicitly expandable/collapsible. Tabs and
navigation scroll within their own rows when needed; edge filters and impact
controls wrap. The time axis wraps instead of widening the page. Long resource
and analysis content remains scrollable independently of the canvas.

Responsive checks should cover narrow and desktop layout bounds, without
horizontal overflow in the page or analysis panel. The desktop side panel is
400 pixels wide. Resizing and canvas filtering must not issue another analysis
request. Selection, path expansion, truncation, incomplete-empty direction
changes, consent reset, and JSON/HTML capture fidelity remain test requirements.

Portable shell integration tests cover narrow/desktop focus and inertness,
one controls region inside the result panel, parameter changes, consent reset,
Escape/button exit, return to the canvas, and same-node reopening after close.
DOM-based tests do not replace browser geometry checks or establish
cross-browser/device, real-cluster, performance, or release acceptance.
M3 delivery review and M4 candidate gates remain separate.

### Portable checks and remaining gates

M0 adds only portable synthetic fixtures, Go/Jest characterization tests, and
this contract. Run from a normal checkout without Kubernetes credentials:

```bash
go test -short -race ./pkg/graph/analysis ./pkg/extractor
go test -short -race ./pkg/graph/... ./pkg/store/...
go test -short -race ./pkg/api -run 'TestImpact|TestOpenAPI|TestAPIVersions'
go test -short -race ./pkg/impact ./cmd/kubectl-atlas
go test -short -race ./pkg/operations ./pkg/snapshot ./pkg/discovery ./pkg/multicluster ./cmd/kubeatlas
go test -short -race ./pkg/crd -run '^TestCRDCoverage'
go test -short -race ./pkg/crd -run '^TestCRDReconciliation'
go test -short -race ./pkg/gatekeeper ./pkg/discovery ./pkg/operations ./pkg/graph/analysis -run 'TestGatekeeper|TestDynamicCoverage|TestSourceDiscovery|TestImpactGatekeeper'
go test -short -race ./pkg/discovery ./pkg/operations ./pkg/graph/analysis -run 'TestAPIInventory|TestStartupFiltering|TestImpactAPIInventory'
go test -short -race ./pkg/store/memory ./pkg/snapshot ./pkg/operations ./pkg/graph/analysis ./pkg/api -run 'HistoryBounds|HistoryRetention|ImpactRetention|ImpactAPIRetained'
npm --prefix web test -- --runInBand --runTestsByPath src/lib/blastRadius.test.ts src/lib/blastRadius.reference.test.ts
npm --prefix web run typecheck
npm --prefix web run lint
npm --prefix web test -- --runInBand
npm --prefix web run build
npm --prefix docs run typecheck
npm --prefix docs run build
```

With Docker available, run the focused disposable PostgreSQL+AGE projection
checks (not a release soak or a test against an existing cluster):

```bash
go test -race ./pkg/store/postgres -run '^TestStore_ImpactSnapshot$' -count=1
go test -race ./pkg/store/postgres -run '^TestStore_HistoryBounds$' -count=1
```

Each later implementation step brings its corresponding behavior tests. M1
must exercise Tier 1/Tier 2 parity and compare paths against hand-authored
oracles; structural fixture validation alone is not correctness evidence.
M3 requires at least five complete Web flows and matching CLI/report results.
The six-case desktop matrix above satisfies that local scenario count; the
fifth slice adds focused responsive repair and browser evidence. The source
and synthetic delivery review below records the current M3 gates before
candidate freeze.

The production Web build and a dedicated single-worker Chromium impact suite
are now configured in CI. The suite reuses the shared captures and exercises
the actual application routes, displayed paths/counts, consent and JSON/HTML
downloads, query changes, loading/403 failure, identity mismatch, and federated
cluster switching. Its API and WebSocket transports are synthetic; the
existing live PetClinic suite remains a separate smoke check. See the fixture
README and `npm --prefix web run test:impact-browser` after a production build.
This automation does not close real-environment integration or M4 gates.

### M3 source and synthetic delivery review

The M3 source/evidence review is complete. This status supersedes the historical
pending client/export/review notes in the individual slices above; it is not
candidate or deployed-environment acceptance. [PR #72](https://github.com/lithastra/kubeatlas/pull/72)
records the exact reviewed head and CI results. It was separately reviewed
and merged on October 4, 2026; the main CI passed. This merge does not close
the deployment or M4 acceptance gates.

| Gate | Reviewed evidence and conclusion |
| --- | --- |
| At least five complete Web flows and CLI/report parity | The six-case matrix satisfies the local scenario requirement. Go API tests independently check handwritten graph oracles and the entire captures; CLI tests consume all six in text/JSON/HTML. The live Chromium suite runs all six against the actual Go HTTP server and executes the compiled CLI for JSON/HTML in the same session. Only the six capture clock fields differ between separate requests; every other field must match. |
| Production Web delivery | CI builds with `tsc --noEmit && vite build`. Nine transport-substituted checks run through `vite preview`; seven live checks load the same `web/dist` through the actual Go static handler, routes and middleware. The live suite has no API or WebSocket substitution, and server reuse is disabled. |
| Capture fidelity and consent | Browser replies deliberately retain noncanonical whitespace and a final newline, so reserialization fails exact JSON/HTML assertions. Downloads require consent and make no extra query. Query changes reset consent. HTML has no active/external elements and retains its restrictive CSP. Unit tests additionally cover large integer/timestamp text, hostile strings, duplicate keys and unsupported private payloads. |
| Identity and cluster boundaries | Browser requests check GET, limit 200 and the detail UID; federated topology deliberately has no captured UID. Identity mismatch and 403 hide results/exports. Switching to west clears east, including an in-flight refresh delivered late. Live checks configure the actual `RBACScope`: east-only visibility, missing/unknown token and denied west statuses, Web refresh hiding a previous capture after denial, and CLI failure without creating a report. `TestImpactAPIAuthorizationBeforeEveryRead` independently checks denied requests do not read graph/evidence. Synthetic credentials and rules do not prove an operator's external authentication configuration. |
| Counts, paths and limitations | Shared fixtures keep ordinary and authorization counts separate, expand representative paths, qualify reference-only Secrets, truncation and incomplete empty results. Portable shell/highlight tests cover filter-independent queries, exact ordinary edge highlights and focus/inert behavior. Earlier four-viewport browser evidence and the narrow resource flow remain bounded layout checks. |

The previously identified live-HTTP repeatability gap is closed by
`web/tests/e2e/impact-live.spec.ts`. The opt-in Go test runner reuses the matrix
loader/seed, serves the production bundle and real API at literal loopback
ports 4174/4175, and runs no informer, database or Kubernetes client. It exits
on SIGTERM/SIGINT with a 90-second deadline and bounded HTTP shutdown.
Playwright owns and stops it.
Actual CLI subprocesses use explicit loopback `--server` and an absent
kubeconfig. Server impact counters verify Web downloads issue no extra query
and each CLI report adds exactly one request. Report files have mode 0600;
401/403 commands leave no file. This closes the source/synthetic M3 integration
follow-up, not deployed watch, external authentication or M4 acceptance.

Reproduce without a cluster (after installing locked Web dependencies and
Playwright Chromium):

```bash
npm --prefix web run build
go test -buildvcs=false -c -o bin/impact-browser-server.test ./pkg/api
CGO_ENABLED=0 go build -buildvcs=false -o bin/kubectl-atlas ./cmd/kubectl-atlas
npm --prefix web run test:impact-live
```

Before M4 acceptance, exercise the frozen candidate's actual Web/API/CLI path
under its configured external authentication, including allowed/denied member
scope, expected-UID replacement, known references, initial sync and list/watch
failure qualification on Tier 1 and Tier 2. This deployment evidence is absent;
the green PetClinic smoke and older upgrade jobs do not supply it. Richer API
inventory presentation, complete collector/version coverage and per-member
history/discovery evidence remain the documented limitations, rather than
silently becoming satisfied by the synthetic client review.

M4 verifies upgrade from the latest v1.6 patch, frozen supported Kubernetes
versions, existing API compatibility, Secret sentinel checks, bounded query
performance, immutable public artifacts, and operator documentation. Benchmark
the proposed new-query p95 target of 1 second on a specified 5K resource shape;
freeze hardware, edge density, query depth/limit, and at least 100 samples per
query before measuring. Dense and 10K cases are reported separately. This is
new v1.7 acceptance, not a retroactive pass or reopening of v1.6 issue #26.

Only the final frozen candidate receives a complete **72-hour** observation.
No 168-hour experiment is planned. Freeze workload, warm-up/baseline windows,
sampling/gap bounds, resource-growth limits, recovery exercises, immutable
image identities, and an independent verifier before launch. No unexplained
restart/OOM, normal-load drops, or write failures may pass. Never concatenate
interrupted attempts or retry silently. Elapsed time alone is not acceptance.
Workstation launchers and private evidence stay outside this repository.

Headlamp/Backstage/rules/Action retain independent release scope and receive
compatibility checks where relevant. Backstage remains source-only; upstream
Headlamp catalog work is not a blocker. Krew updates follow real core artifacts.
HA, built-in auth, cloud discovery, attack paths, GitOps drift/remediation,
arbitrary manifest simulation, LLM diagnosis, and blanket dependency-major
upgrades are not part of this release. Older phase guides do not override this
accepted scope.

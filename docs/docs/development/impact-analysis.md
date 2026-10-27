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
2 MiB cap. This initial slice supplied standalone inventory only; the member
worker follow-up below adds production federation wiring. Injected clients
lacking contextual discovery stay unknown.

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
Tier 1, and federation do not start this worker in the original standalone
slice; the member-retention follow-up below adds scoped federation workers.
An observer owns one configured
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
standalone cache. Member retention is added in the follow-up below;
member-specific writer lifecycle qualification and visibility of the existing
legacy history routes remain pending.

Portable tests cover cache-only request reads, blocked queries, cancellation,
shutdown, expiry, failed/invalid evidence, writer-gap independence, and API
disclosure. Shared memory/PostgreSQL contracts cover exact scopes (including
SQL wildcard characters), time boundaries, empty and marker-only windows,
Secret exclusion, pruning, and returned ownership. Disposable PostgreSQL tests
run the aggregate under a role allowed to read only timestamps, scope, and kind,
with no payload/identity/count access. These checks establish retained-metadata
behavior, not real-cluster continuity, recovery, performance, or release acceptance.

### M2 API inventory completed-prefix retention follow-up

Sequential API inventory now retains this round's validated descriptors from
earlier successful version requests when a later request returns a non-404 error.
If at least one descriptor was confirmed, the round is `partial`; without any
confirmed descriptor, the existing `permission_denied` or `failed` classification
remains. The partial state does not encode the failing request's specific error
category. Rows accompanying an error are excluded. Discovery stops at that error
without retry, fallback or another version request. Existing 404 handling and
malformed metadata/limit qualification remain unchanged.

Cancellation is checked immediately after group and version discovery returns,
before handling a success or error. A cancelled or expired context returns a
failed empty round, even when the dependency returns rows or a permission error.
The worker's existing lifetime check still prevents publication after member
removal/shutdown. Aggregated stale/failure metadata follows the separate retention
rules below and can never qualify a complete round.

Recording a new round replaces prior evidence. Only completed descriptors from
the current round can survive its later failure; old successful rounds are not
merged into fresh partial evidence. A subsequent complete round replaces the
prefix normally. Partial descriptors remain available in the captured before/
after inventory, but optional API assessments stay `unknown`, no unobserved-API
list is inferred, and absence or complete collector coverage is not established.
Raw errors and failed-response rows never enter the evidence or analysis response.
Member scope, ownership and the existing freshness/stopped/limited checks remain.

The five-minute interval, ten-second round deadline, 64-version and 256-descriptor
limits are unchanged. This adds no worker, Kubernetes object read, graph query,
public field or GraphStore method. Portable regressions cover each failure
position, permission/transport errors, failed rows, cancellation during discovery,
old-round replacement, member/standalone recording, real analysis capture and
complete-round recovery. A loopback client-go HTTP test confirms retained metadata
after a later 403 without extra requests or object reads. These local checks do
not qualify real-cluster discovery reliability or M4 acceptance.

### M2 aggregated API inventory partial retention follow-up

When contextual aggregated discovery returns a failure set alongside resource
metadata, inventory now retains valid descriptors already returned for advertised
versions outside that set. It uses the same descriptor validation, scope handling,
subresource exclusion and 256-descriptor cap as sequential discovery. Failure-set
membership rejects a version even if its error value is nil or a resource list is
also present. Missing/nil lists, mismatched versions, malformed rows and map entries
outside the advertised version list do not become retained endpoint evidence.
The round remains `partial` even if some returned versions are usable.

This path makes no per-version request, fallback or retry. It only consumes the
bounded metadata already returned by the discovery call. Cancelled calls and
top-level discovery errors expose no resource descriptors. For mixed legacy/
aggregated responses, client-go may return a nil resource table; that stays partial
and empty without fetching replacements. A nil group list likewise supplies no
advertisement proof. The complete/no-failure discovery path keeps its existing
sequential resource requests.

The pinned client-go v0.37.1 parser removes stale versions from advertised version
lists, sometimes leaving empty groups. Such empty groups are skipped only when
the validated failure set accounts for their group; unexplained empty groups and
malformed/duplicate advertisements remain invalid. Core/v1, failed versions and
healthy advertised versions share the existing 64-version round budget. Failure
keys are validated and bounded before traversal, and cancellation is checked
during metadata handling. Exceeding the version budget returns limited partial
evidence without expanding work. The ten-second round deadline and five-minute
schedule remain unchanged.

Recorded partial descriptors remain visible in before/after analysis capture.
They do not qualify optional API presence/absence, unobserved endpoint lists or
complete collector coverage. Member scope, copied evidence, raw-error exclusion
and old-round replacement remain in place. There is no new public field, worker,
Kubernetes object read or GraphStore method.

Portable tests cover healthy/stale overlap, stale-only groups, nil/missing and
malformed metadata, failure-key validation, combined failure/healthy version caps,
descriptor limits, cancellation, member/standalone recording and real analysis
capture. Local client-go HTTP tests exercise aggregated v2, stale core versions
and mixed legacy/aggregated responses, asserting only `/api` and `/apis` requests.
These checks do not establish real-cluster discovery reliability or M4 acceptance.

### M2 startup endpoint shape validation follow-up

Startup selection now requires every canonical core endpoint to advertise the
expected Kind and namespace scope as well as its exact resource name and existing
list/watch verbs. Namespace, ClusterRole/ClusterRoleBinding, ClusterPolicy and
ClusterPolicyReport endpoints are cluster scoped; the other canonical endpoints
are namespaced. Unsupported optional endpoints are skipped, while unsupported
required endpoints stop selection. This affects the existing standalone server
and local-UI CLI startup filter; it does not add or broaden any watched endpoint.

All requested targets, including custom GVRs, also require valid descriptor
identity. Explicit row group/version fields must match the requested endpoint;
omitted fields retain their existing discovery meaning. Missing/invalid Kind or
duplicate target rows are errors, including identical duplicates and a duplicate
after an initially supported row. These failures return no partial startup set,
even if an earlier required endpoint passed. Unrelated rows and subresources do
not expand the requested scope or invalidate a different target. Custom endpoints
outside the canonical set keep shape compatibility without inferring a Kind or
scope from the plural resource name.

The existing group-version response cache still performs at most one discovery
request per requested version, with the same 256-input cap, five-second per-query
context and synchronous legacy-client compatibility. Cancellation is checked
during target scanning. No retry, fallback, worker, Kubernetes object read, graph
write, public field or GraphStore method is added. Passing this metadata filter
does not establish effective authorization, successful list/watch delivery,
complete collector coverage or optional API installation.

Portable regressions independently specify all 25 canonical Kind/scope pairs,
cover required/optional failure policy, malformed and duplicate metadata,
same-version cache reuse, atomic selection failure and custom scope compatibility.
Local client-go HTTP tests confirm exact metadata paths and one request per check
without object reads. Existing injected discovery fixtures now carry realistic
Kind/scope metadata instead of placeholder Kind values. These tests do not qualify
real-cluster startup behavior, watch reliability or M4 acceptance.

### M2 Gatekeeper template discovery identity follow-up

The separate Gatekeeper ConstraintTemplate discovery gate now validates the
entire returned target set before accepting an advertised API. It requires one
`constrainttemplates` descriptor with the supported `ConstraintTemplate` Kind,
cluster scope and list/watch verbs. Explicit row group/version fields must match
`templates.gatekeeper.sh/v1`; omitted fields remain compatible. Duplicate targets
are rejected even when identical, contradictory only in verbs/scope, or placed
after an initially supported descriptor. Unrelated rows and subresources do not
broaden the target or invalidate a supported descriptor.

Ambiguous/inconsistent metadata returns the existing `failed` discovery state,
not `advertised` or `not_advertised`. With a discovery client configured, the
existing wait loop therefore does not start the ConstraintTemplate informer or
its generated constraint collectors until a later supported response. The loop
keeps its existing polling interval; each check still uses one contextual
discovery request and the existing five-second deadline. Target scanning now
also rechecks that context. Synchronous legacy clients retain their existing
compatibility path without an abandoned goroutine. The no-client path remains
unconditional observation with unknown API evidence, rather than advertisement
proof.

Each result retains its member/standalone source identity and sanitized evidence.
A later valid check advances that discovery revision without clearing earlier
list/watch, processing or persistence gaps. Failed checks do not clean retained
graph data. No impact-query probe, extra watch, worker, Kubernetes object read,
public field or GraphStore method is introduced. Advertisement remains distinct
from installation, effective authorization and successful observation.

Portable regressions cover both scopes, implicit/explicit identities, both
conflict orders, late duplicates, unrelated metadata, recovery revisions and
sticky persistence gaps. Fake-client lifecycle tests prove that ambiguous
metadata starts no list/watch, that a later valid response enables the existing
collectors, and shutdown still joins them. Local client-go HTTP tests assert one
exact discovery read without object requests. Real-cluster Gatekeeper behavior
and M4 acceptance remain pending.

### M2 API inventory endpoint identity follow-up

API inventory no longer retains an arbitrary first descriptor when a discovery
response contains repeated top-level resource names for the same group/version.
Every candidate for that endpoint is excluded, even for identical duplicates or
when the other row has an invalid Kind or inconsistent explicit group/version.
Both row orders receive the same qualification. Subresources remain excluded;
the same resource name in another group/version is independent. Other valid
descriptors remain available, and the round is partial rather than proof of API
presence, absence or complete observation.

Selection retains at most the remaining portion of the existing 256-descriptor
budget. A second contextual scan of that returned list counts only selected
names, saturating at two occurrences, then removes ambiguous candidates. This
also detects duplicates after selection reaches the cap without retaining
additional identities. A capped round stays limited, starts no later version
request, and does not backfill slots freed by exclusions. Cancellation discards
the round. The 64-version budget, ten-second round deadline and five-minute
worker interval remain unchanged; no object read, fallback, hot-path query or
public schema change is added.

Portable regressions cover sequential and partial aggregated discovery, member
and standalone scope, Kind/scope/verb/identity conflicts, invalid rows before or
after valid rows, duplicate counts beyond a byte counter's range, subresources,
independent names and late duplicates after the descriptor cap. Recorded impact
captures preserve only the scoped current round and leave optional API
assessments unknown for partial evidence. A later complete response replaces the
capture without merging older rows. Local HTTP tests use the real client-go
parser and assert exact discovery paths without object requests. These checks
do not establish real-cluster discovery behavior or M4 acceptance.

### M2 member-scoped API inventory follow-up

The production federation path now creates a discovery client from each
member's own kubeconfig, alongside its dynamic client. Each member informer
owns the existing bounded API metadata worker and records inventory under its
operator-selected cluster ID. It never substitutes the management cluster's
inventory. Impact requests continue to authorize member scope before reading
cached evidence; no discovery is added to the request path or wire schema.

The worker retains the standalone limits: a ten-second round, five-minute
polling interval, ten-minute evidence expiry, at most 64 group versions and
256 endpoint descriptors. It reads discovery metadata only, never Secret
objects or other object collections. A member's discovery denial or failure
records its own sanitized qualification without stopping sibling informers.
Missing contextual discovery remains unknown. Discovery success does not
clear list/watch, handler, persistence, or other observation gaps.

Removing a member, stopping the manager, or canceling its parent context
cancels and joins the member worker before its informer returns. Removal and
shutdown retain the member name until workers finish, preventing an old
inventory worker from overlapping a same-name replacement. Reattachment then
starts a new inventory generation. Existing dynamic-only factory users remain
supported and do not construct a discovery client.

Portable race tests exercise two member-specific loopback discovery endpoints,
one advertised inventory and one denied member, cached reads without new
requests, no object/Secret requests, management/hidden-scope isolation,
removal and reattachment, failed/unsupported discovery, in-flight cancellation,
and same-name exclusion while workers drain. These are local implementation
tests, not real-cluster or M4 acceptance. This slice adds API inventory only;
the next follow-up adds member CRD/Gatekeeper collection. Complete collector
coverage, version equivalence, and per-member history qualification remain open.

### M2 member-scoped CRD and Gatekeeper collection follow-up

Production federation now gives each member one owned bundle containing its
core/inventory informer, CRD discovery, and Gatekeeper discovery. All three use
the member's own dynamic/discovery clients. CRD instances and constraints carry
the member cluster ID before persistence or deletion, and CRD and Gatekeeper
source evidence remains independent under that same member scope.

The CRD collector retains served-version selection, CRD UID/shape reconciliation,
per-type observation tokens and existing resource/edge retention semantics.
Rego results are copied into the member's identity space; explicitly foreign
edge endpoints are rejected with an extraction gap. The shared evaluation
cache includes cluster ID, full API version, UID, resourceVersion, rule hash and
entrypoint, so matching UID/version strings in different members or API
representations cannot reuse one result.

Gatekeeper uses that member's supported-template discovery check and existing
dynamic Constraint registration. Extractor reads are restricted to the owning
member even if an extractor omits or changes its filter; emitted edges must
also stay in that member. Discovery absence/denial, failed list/watch, processing,
extraction and write gaps keep their existing explicit qualifications. A
permission failure does not become evidence of absence or stop another member.
The worker never requests Secret objects. Existing loaded rule packs remain the
configured startup set; this change does not add per-member pack acquisition.

Member removal, parent cancellation and shutdown cancel and join the bundle's
collectors, including running and retired dynamic informer callbacks. The member
name is not available for reattachment until those callbacks finish. An early
collector exit cancels and joins its own bundle and is recorded as a member
error; it does not cancel sibling members. Shared dynamic metrics now sum the
registered informers across members instead of being overwritten by each manager.
No new discovery request enters the impact handler, and no v1alpha1 or impact
wire-schema change is made.

Portable race tests cover two same-name member datasets, CRD and Constraint
identities/edges/deletes, independent source evidence, denied member discovery
and lists, no Secret requests, scoped extractor reads, rejected foreign edges,
cache separation, aggregate metrics and blocked-callback shutdown. The production
member factory is exercised with fake Kubernetes clients, not a live cluster.
These checks do not establish complete collector coverage, served-version
equivalence, Gatekeeper policy effectiveness, missed-delete repair, member
history continuity, or real-deployment/M4 acceptance.

### M2 member-scoped missed-delete repair follow-up

Production member bundles now reconcile retained graph rows for actively
registered canonical core endpoints, selected CRD endpoints and generated
Gatekeeper Constraint endpoints. After initial handler delivery, each endpoint
runs an immediate repair round and repeats every five minutes. This repairs
objects deleted while a member was detached/restarted and delete writes that
failed after their informer callback had already been consumed. Ordinary local
cache resync still does not relist Kubernetes or replay a missing delete.

Each round captures candidates from the owning member and exact Kind/API version
before requesting a fresh all-namespace list with empty `resourceVersion`, no
selectors and page size 500. Every continuation page must succeed with the same
nonempty snapshot version before any deletion is allowed. Incomplete, denied,
expired, malformed or over-limit lists cannot authorize a sweep. Work is limited
to one repair round at a time per member, a ten-second deadline including queue
and delivery-lock waits, 10,000 candidates/listed objects and 32 pages. Repair
does not change informer list/watch options, require new Kubernetes verbs, or
run inside an impact request.

Only missing names become deletion candidates. Same-name presence of any UID
protects a replacement. Atomic store deletion compares member identity, API
version, UID and resourceVersion with the captured candidate, then cascades
incident edges in the same memory critical section or PostgreSQL/AGE transaction.
Rows changed during the list are retained for a later round. GraphStore v2's
method set and the public wire schemas remain unchanged; a store without the
optional conditional-delete capability records a persistence gap and does not
fall back to an unsafe read followed by unconditional deletion.

Object callbacks and repair deletion share a member-local identity lock through
the complete resource/extraction/edge-write pipeline. An in-flight extractor
cannot write incident edges after repair deletes its resource. Other object
identities remain independent, and lock entries are released after their last
owner/waiter. Registration cancellation stops its repair work; bundle shutdown
joins those workers before the member name is reusable. Core repair emits a
delete notification/history enqueue only after a committed deletion, at repair
detection time. A failed ordinary core delete no longer emits a success receipt.

Failed repair reads, malformed lists and work limits retain a sticky processing
gap; failed graph reads/deletes or unavailable conditional deletion retain a
persistence gap. Successful later repair does not reset earlier coverage gaps,
assert a drained watch backlog, or reconstruct the time of an upstream deletion.
Unversioned/reference-only nodes, Secrets, different API versions, unregistered
or retired endpoints, and other members are not swept. CRD/template disappearance
and API unavailability alone are not proof that retained instances are absent.

Portable race tests exercise the production member factory across reattachment,
all three collector paths, member and standalone isolation, retained older API
versions, complete pagination, page failure/expiry, malformed/limited lists,
same-name replacement and concurrent updates, failed watch-delete repair on a
scheduled round, success-only receipts, sticky gaps, bounded concurrency,
blocked extraction and cancellation/join. Both store backends implement the
same conditional-delete contract tests; PostgreSQL/AGE execution requires the
separate container-backed test environment. These local tests do not establish
real-cluster reliability, cross-version or retired-endpoint cleanup, complete
collector coverage, member history continuity, or M4 acceptance.

### M2 member-scoped retained history metadata follow-up

The Tier 2 federation profile with history enabled now gives each member bundle
an independent retained-metadata observer. Construction alone neither registers
a scope nor queries history. Starting the bundle publishes a new empty cache
for its exact member name, samples event/marker bounds, and repeats every five
minutes. Member removal and bundle shutdown cancel and join the observer,
including an in-flight query. The same name stays reserved until that lifetime
has returned; reattachment starts with `not_queried` evidence rather than the
previous member cache. A stopped observation retains its stopped flag, checked
window and freshness qualification. It does not become current on request reads.

All observers use the existing optional `HistoryBoundsReader` capability with
the named member and configured retention window. At most two member metadata
reads run concurrently; waiting for that shared budget is included in the
existing five-second read deadline. The registry retains at most 128 distinct
member scopes, including stopped entries. Same-name replacement reuses its
slot. An over-capacity member remains running but its uncached history metadata
is `unsupported`, without a query or eviction of another member's evidence.
Missing scoped storage capability is also `unsupported`; there is no fallback
to unlabelled/global event or marker APIs.

Impact requests read a fixed-size owned cache through an immutable published
index. They do not wait for a membership mutation or running query, register a
new observer, refresh stale evidence, or read the global writer counters.
Authorization and attached-member checks still precede all impact evidence
access. A member's `retentionEvidence.bounds.clusterId` must match the selected
scope, and window, retention, durability, timestamp and freshness checks remain
mandatory. Failed queries clear previous successful bounds; a failed or blocked
member cannot supply evidence for another member.

The existing `availability.evidence.history` shape now permits exact-member
retention metadata independently of recording qualification. With these caches,
`retentionNanos` is the configured window and `retainedData` reflects the cached
metadata state. In this original metadata-only slice the parent history state
remains `unsupported` with reason
`history_cluster_scope_unavailable`, coverage remains `unknown`, and the writer
observation-start timestamp remains zero. These fields mean that member-level
recording lifecycle is not qualified; successfully reading retained timestamps
does not qualify the shared writer, reset a recording gap, establish complete
history or verify a recovery point. Existing unlabelled markers remain excluded
from named-member reads. Legacy history-route visibility remains separate work.

Portable race tests cover member/standalone/hidden-scope separation, Secret
exclusion, bounds and payload/count non-disclosure, failed/unsupported and stale
caches, fresh reattachment, duplicate exclusion while a query drains, cancellation
of queued reads, bounded concurrent reads and registry capacity. Actual member
factory tests verify ownership and joins. Actual impact-handler tests verify
allowed responses, denied requests before cache access and repeated cache reads
without database queries. These local checks do not establish deployed Tier 2
performance, member writer loss accounting, continuous retained history,
restorable snapshots, legacy history-route authorization or M4 acceptance.

### M2 member-scoped recording lifecycle and loss follow-up

Production Tier 2 members with history enabled now bind their core informer
history sink to a member recording session before starting collectors. Initial
core delivery, core updates/deletes and committed core missed-delete repair
events use that session. The session observes its own start/cancellation/close,
queued and in-flight delivery, queue loss and exhausted-write loss. It does not
infer a member lifecycle or loss from global writer metrics. This slice initially
covered core events only; the following dynamic-history follow-up extends its
member ingress. No complete history scope is asserted.

The existing shared queue, configured worker count and per-event retry/backoff
budget remain unchanged. A queue item carries its exact recording token. When
overflow discards the oldest item, queue loss is recorded on the discarded
item's member, rather than the incoming event's member. A rejected incoming
event or exhausted store-write budget marks its own token. Foreign-cluster
events are rejected, not silently retagged. Queued events remain metadata-only;
the recording registry retains no object names, UIDs, namespaces or payloads.
Global Prometheus counters retain their previous aggregate meaning.

Bundle cancellation marks its recording session stopped. After collectors have
joined, closing the session stops accepting delivery and waits for its own
queued/in-flight writes. It neither closes the shared writer nor stops another
member's session. The member name remains reserved until these writes finish,
including a blocked sink that ignores cancellation. Same-name replacement gets
a new start interval and pending-delivery count while inheriting previously
known loss flags. Obsolete closed tokens cannot enqueue into or mutate the new
session. Later successful writes, retained metadata reads and reattachment do
not reset a known history gap.

At most 128 member recording scopes, including stopped entries, retain qualified
state in the process. Same-name replacement reuses the entry. Beyond that cap,
events may still use the shared queue, but member recording remains explicitly
unsupported rather than allocating an unbounded history registry or stopping
graph collection. Legacy direct writer calls without a member token cannot
qualify a member's lifecycle or loss. Disabled history and Tier 1 remain outside
this feature.

The existing impact history states now describe registered member sessions:
`recording` means the member sink and shared writer are running; `degraded`
records that member's known loss or unavailable storage; `stopped` retains its
shutdown and loss qualification. `history_delivery_pending` includes its
in-flight writes even when the shared queue appears empty. History coverage
remains `unknown`, or `partial` for known delivery loss, never complete. Without
a registered recording session the previous unsupported member qualification
remains. Retained metadata is independent in all of these states, and exact
member/window/durability/freshness validation remains mandatory. No v1alpha1,
GraphStore v2 method-set or public wire-shape change is made.

The writer now also serializes queue closure against non-blocking sends and
ignores duplicate Start calls, preserving the configured worker budget and
preventing a concurrent Stop/Enqueue from sending to a closed channel. Portable
race tests exercise overflow ownership, scoped retry exhaustion, pending writes,
foreign-event rejection, sticky loss on reattachment, stale-token exclusion,
capacity, writer cancellation, concurrent shutdown and worker-budget preservation.
Production member-factory and actual impact-handler tests verify ingress binding,
per-member drain/name exclusion, independent retained metadata and cache-only
authorized disclosure. These establish local behavior, not deployed write
reliability, complete dynamic-source history, continuous retention, verified
recovery points, legacy history-route authorization or M4 acceptance.

### M2 member dynamic-source history follow-up

Production member bundles now supply one shared `ResourceHistory` to core,
CRD-instance and Gatekeeper-constraint collectors, bound to the member recording
token described above. A collector holds its existing member resource lock
through graph receipt lookup, resource persistence, metadata enqueue and edge
extraction. The graph's current UID/resourceVersion is the receipt; no extra
object cache, queue, worker, Kubernetes API call or retry policy is introduced.
All database work remains in background collection rather than the impact query
path. GraphStore v2 and public response schemas remain unchanged.

Only successful graph writes with verifiable UID/version identity can enqueue
an add/update. A receipt-read failure does not establish a baseline and therefore
stops that delivery with a persistence gap. A failed resource write does not
advance the receipt or enqueue history. Unchanged cache replay and overlapping
collectors observing the same current UID/version do not write another resource
or event, while edge extraction still runs to repair target-arrival or earlier
edge-write failures. A served-version switch updates the graph representation
without manufacturing an object change. An Add callback for a previously stored
UID with a new resourceVersion records an update; replacing a known UID records
an add. Without a stored baseline the callback supplies the observation type;
none of these events prove the Kubernetes creation time.

Watch deletes read the stored incarnation and use the existing optional atomic
`ResourceVersionDeleter` capability. Both stores already implement it. A failed
read/delete enqueues no event; absence or an old-UID tombstone is a no-op, so a
same-name replacement survives. A tombstone for the same UID may have an older
resourceVersion or served version; successful deletion records the last stored
receipt. Both dynamic collectors also enqueue deletes after their existing
missed-delete workers commit under that lock. Overlapping repair/watch deletes
therefore record only the successful removal. Retired CRD/template registrations
retain graph rows and do not imply deletion. Unversioned objects can still be
stored and extracted, but leave a processing gap and produce no history event;
unverifiable deletes retain the row for future qualified repair.

Events carry only member/resource identity metadata, never specs or status.
They reuse the same bounded queue, workers, retry budget, member pending/loss
accounting and drain/name-reservation lifecycle. The recording registry still
contains no object identities. An unchanged object retained through member
reattachment does not produce a fresh creation event. This does not establish
that its earlier history was delivered: graph persistence and history enqueue
are separate, and retained metadata cannot erase known loss.

These receipts compare only the current graph version. Kubernetes resourceVersions
are opaque; separate watches may observe different versions out of order. This
feature neither sorts those versions nor retains a complete historical deduplication
ledger. History coverage remains unknown, or partial with known delivery loss.
Standalone dynamic collectors, continuous retained history, recovery verification,
legacy history-route authorization and M4 cluster acceptance remain outside this
slice. Portable tests cover write/read/delete failures, replay extraction,
served-version changes, UID replacement and stale tombstones, concurrent shared
receipts, actual production member-factory overlap, CRD/Gatekeeper repair history,
member isolation, retirement without fabricated deletes and same-name reattachment.
They establish local source/synthetic behavior, not deployed Tier 2 qualification.

### M2 standalone dynamic-source history follow-up

History-enabled standalone startup now constructs core, CRD-instance and
Gatekeeper collectors through one production component factory. They share an
explicit `NewStandaloneResourceHistory` binding, the existing standalone writer
and one callback-lock coordinator. Empty cluster identity selects only
unlabelled graph rows; passing an empty member name to `NewResourceHistory`
remains invalid. Named rows, even with the same UID, are rejected by that binding
and are neither modified nor assigned standalone history. Legacy unlabelled
records retain their unknown provenance; no named-member attribution is inferred.

The writer is supplied only when `runWatch` has enabled Tier 2 snapshots. With
history disabled or on Tier 1, dynamic collectors keep their previous graph-only
path and no new history sink or coordinator is allocated. Memory-backed tests
are fixtures, not a Tier 1 snapshot feature. No member-recording session is
registered for empty scope: the existing standalone writer lifecycle, queue,
worker/retry budget, pending/loss accounting and shutdown drain remain in use.
All three component lifetimes are joined before the writer is stopped.

Callbacks hold the shared resource lock through current graph-receipt lookup,
resource write, metadata enqueue and extraction. Unchanged UID/resourceVersion
replays do not write another event; extraction still runs. Served-version
representation changes, UID replacement, read/write failures and cancellation
follow the same receipt contract as named members. Watch deletes use the stored
incarnation and optional atomic `ResourceVersionDeleter`; duplicate/old-UID
callbacks do not delete a replacement. Missing conditional-delete capability
fails closed without unconditional fallback. Events contain identity metadata,
not specs/status or Secret contents. These observations do not establish an
upstream creation timestamp or complete retained history.

At this history-only stage, the existing `MemberRepair` coordinator supplies
locks in this scope, but its `Run` rejects empty member identity before listing,
scheduling or repair work. The standalone repair follow-up below adds explicit
standalone binding rather than treating an empty member name as authorization.
This history-only slice introduces no new watch, fresh list, Kubernetes write,
query-time database work, tombstone ledger or background worker pool. Cleanup
across retired APIs remains separate work. Independent
watches can still deliver out of order; graph receipts do not provide global
ordering or historical deduplication, and a late upsert can temporarily restore
a deleted node. Writer or observation gaps are not cleared by a successful replay.

Portable contract tests now exercise named and standalone scopes for receipt
failures, replays, concurrent overlap, served-version changes, UID replacements,
tombstones and metadata-only events. The actual production-factory fixture runs
history enabled and disabled, confirms unchanged initial List/Watch counts,
records the expected adds/updates/delete, recreates the collectors against
retained graph receipts and reads the result through the scoped event reader.
The same-UID named row is preserved and disabled history produces no events.
These are fake-client/in-memory race checks, not PostgreSQL deployment, durable
crash recovery, continuous-history or real-cluster acceptance evidence.

### M2 standalone missed-delete repair follow-up

The production standalone factory now shares one `NewStandaloneRepair`
coordinator across core, CRD-instance and Gatekeeper collectors, including
overlapping registrations. Its explicit standalone binding accepts only
unlabelled graph rows; `NewMemberRepair` with an empty member name still cannot
run repair. Named-member rows are excluded both by the scoped store read and
the returned-row check. No legacy provenance is inferred.

Repair is independent of Tier 2 history: Tier 1 and history-disabled collectors
also repair their current graph without enabling snapshots. With history enabled,
the same standalone writer receives a metadata-only detection-time delete only
after committed conditional removal. Overlapping collectors and restarts cannot
emit another repair delete for an already absent row. This is not a Kubernetes
deletion timestamp, a continuous history guarantee, or a recovery point.

Each active registration waits for initial handler delivery before its first
round, then runs every five minutes. All standalone collectors share one repair
slot. Each round keeps the existing ten-second timeout, 500-item pages,
10,000-item candidate/list bounds and 32-page ceiling. Repair uses fresh,
unfiltered list snapshots and validates every page before any deletion. Denied,
failed, inconsistent, malformed, limited, cancelled or incomplete lists never
authorize removal. Secret endpoints are excluded. No watches, Kubernetes writes,
analysis-time database work, writer queues or worker pools are added.

Candidate rows are captured before listing and must match the active kind and
group/version and carry a valid UID/version receipt. Conditional deletion
compares that receipt atomically under the shared callback lock, preserves any
same-name live object or changed stored receipt, and cascades incident edges.
Missing atomic-store capability fails closed. Collectors join their repair
workers on shutdown; cancelled rounds cannot delete retained candidates. A
successful round never clears sticky observation or writer gaps. Unversioned
legacy rows, retired endpoints and other API versions remain untouched.
Independent-watch ordering and historical deduplication remain unqualified.

Portable race tests cover pagination/failures, member isolation, receipt changes,
metadata-only committed deletes, callback/extraction serialization, initial-sync
and shutdown joining. Production-factory fake-client tests exercise core, CRD
and Gatekeeper repair with history on/off, overlapping constraint sources and
restart, while preserving retired/unversioned/member rows and read-only API
requests. These checks do not establish deployed PostgreSQL behavior, actual
Kubernetes list/watch reliability, continuous history or M4 acceptance.

### M2 still-served version repair follow-up

CRD collectors now supply the selected registration's current served-version set
to the existing shared repair coordinator. Retained rows at a version whose
watch was retired can be checked only while that version is still explicitly
served by the current CRD. Each retained representation is compared against a
fresh, complete list from its own exact group/version/resource, kind and current
scope. Retained rows from a different scope are preserved with a processing gap;
wrong-scope list items invalidate the round. An
empty selected-version list is never evidence that another version is empty.
Endpoint snapshot versions are validated independently, not treated as one
cross-version Kubernetes transaction. This does not establish conversion
equivalence, CRD-incarnation provenance for legacy rows, or a historical ledger.

The selected endpoint remains the only CRD instance watch. The repair round
captures one owned, sorted served-version snapshot; changing only that set does
not restart the selected watch or reset its coverage token. Its metadata lifetime
cancels an in-flight round when the set changes. Registration replacement and
retirement cancel the old repair lifetime; shutdown joins the existing workers.
The next scheduled round reads the updated set; no extra repair timer or immediate
retry is added.

All endpoints share the existing one-slot, ten-second round deadline, 10,000
candidate and listed-item limits and 32-page ceiling. At most 32 served versions
are accepted per round. Invalid, duplicate, missing-selected or oversized sets
fail closed with a processing gap. Only versions with valid retained candidates
are listed. Every required endpoint and continuation page must validate before
any deletion is attempted; a 404, denied/failed/partial response or total work
limit retains all candidates for that round. Existing atomic receipt checks,
callback locks, incident-edge cascading and committed metadata-only history
receipts are unchanged. Failures qualify the active registration; auxiliary
lists do not make a stopped/unregistered version's watch evidence healthy.

This applies to member and explicitly bound standalone CRD collectors, with
history enabled or disabled. Core and Gatekeeper-specific workers keep their
existing single-endpoint repair; a generated Constraint CRD seen by the CRD
collector follows the CRD path. No Secret access, Kubernetes write, store schema,
public API, GraphStore v2 method, history queue or worker pool is added.

Fully unserved/deleted CRDs, stopped templates, retired kinds/scopes and versions
not declared served remain outside automatic cleanup. Disappearance or API
failure never fabricates object deletion or clears existing gaps. Portable
tests cover independent old/new snapshots, scope isolation, combined budgets,
all-lists-before-delete behavior, changed receipts, committed history, selected
watch preservation and mid-list version revocation. These fake-client/in-memory
race checks do not establish real-cluster or deployed PostgreSQL acceptance.

### M2 fully retired source retention and invalidation follow-up

The accepted retirement policy is **retain and qualify, never infer deletion**.
Stopping a collector, removing a CRD/template, receiving an API 404 or ageing a
cache does not remove graph resources/incident edges, append deletion events or
clear known observation/history loss. There is no TTL garbage collection for
these records. Resumed, explicitly served endpoints may still prove absence
through the bounded fresh-list repair above; otherwise data stays retained.

New CRD-instance and Gatekeeper writes can carry a private last-writer receipt:
exact member (or explicitly unlabelled standalone scope), source, metadata owner
name/UID, endpoint/kind/scope, random registration token, and object UID/version.
It is stamped only by the matching live type token, not from Kubernetes payload
fields, labels, annotations or same-name inference. The receipt is stored with
the resource in the same memory operation or PostgreSQL/AGE transaction. Tier 2
uses an optional private JSONB-envelope field, without a new table or migration.
The receipt is not Kubernetes ownership, a complete source ledger, or proof of
current object existence. Legacy records are never backfilled by endpoint name.

| Qualification | Meaning and policy |
| --- | --- |
| `registered` | The stored writer receipt matches the current registration. Watch health, per-object freshness and historical completeness still require their separate evidence. |
| `retired` | That exact writer registration is known stopped. Keep the graph data and qualify its observation; do not manufacture an object deletion. |
| `replaced` | The current endpoint belongs to another registration/owner/shape. The new lifetime cannot adopt the old receipt without fresh object delivery. |
| `unknown` | Receipt absent, invalid, foreign, ambiguous, capacity-limited or lifecycle knowledge unavailable. Retain data without attributing an owner. |

Lifecycle metadata shares the existing per-cluster/source/type bounds; no
per-object side ledger, worker, timer or queue is added. It is process-local.
After restart, stored Tier 2 receipts survive but lifecycle knowledge is unknown
until registration/delivery supplies new evidence. Even a same-name/same-UID
owner receives a new registration token. A fresh callback can refresh the private
receipt for an unchanged object without creating another resource-history event.
Overlapping sources do not retag an unchanged receipt to erase its last writer's
limitations; this is not all-source delivery accounting. Source replacement and
bundle shutdown keep the existing shared callback locks and worker joining.

Gatekeeper registrations now bind template UID when available. A template
kind/UID change retires its stored old endpoint; old-UID or unidentified
tombstones cannot retire a proven replacement. A matching template UID resolves
the stored endpoint even if the deleted spec is obsolete. Missing legacy owner
identity may keep collection working but cannot attest provenance or replace an
explicitly owned endpoint.

Gatekeeper callbacks recheck registration cancellation after acquiring the shared
resource lock, after resource persistence and extraction, and before each edge
write. Template replacement, retirement and shutdown therefore stop subsequent
callback stages when an in-flight dependency returns after cancellation. An
already-started store operation can still commit if it ignores cancellation;
there is no rollback or compensating deletion of retained data. The replacement
callback uses the same resource lock and commits its own receipt after the old
callback drains. Cancellation errors do not create new persistence/extraction
gaps or clear prior gaps; actual non-cancellation failures retain their existing
gap reporting. Object history still describes committed writes, not metadata
retirement or a guaranteed upstream event order.

Fake-client manager regressions block resource persistence, extraction or the
first edge write across replacement, retirement and shutdown. They cover named
member and standalone scope with history enabled/disabled, distinguish committed
in-flight edges from forbidden subsequent writes, retain resources and source
qualification, and join callbacks at shutdown. Separate cancellation-error
regressions cover resource/edge persistence, extraction and deletion while
preserving existing loss evidence and committed-history counts. These are local
race checks, not real-cluster or deployed storage acceptance.

The private receipt never enters resource JSON, v1alpha1, portable graph exports,
history payloads or Secret references. Invalid private storage metadata degrades
to unknown without hiding the underlying resource. Impact projections retain a
bounded internal receipt; PostgreSQL selects only its allowlisted scalar fields
and charges it to the existing projection byte budget. Conditional graph deletion
also compares the stored writer receipt, so an older repair candidate cannot
delete a freshly observed registration with otherwise unchanged UID/version.
GraphStore v2 and public response schemas remain unchanged.

For the root and returned resources, observed impact analysis checks cached
source lifetimes before/after its existing immutable graph read. It retains the
same nodes/paths and adds `retained_source_unknown`, `retained_source_retired`,
`retained_source_replaced` or `retained_source_window_unstable` qualifications
where applicable. The API/client/text/JSON/HTML path preserves these limitations
without exposing owner identities or making another query. This is not a global
retained-object inventory or a distributed transaction. Core coverage keeps its
existing static requirements; Secrets remain reference-only.

Portable tests exercise source retirement/replacement/restart, metadata-only
receipt refresh, member isolation, unowned legacy data, scope/UID/version
mismatch, projection budgets, invalid private-envelope decoding, template
handoff and late tombstones, and actual API/client/report preservation. Shared
memory/PostgreSQL contracts and PostgreSQL scalar-projection checks are included;
container-backed SQL execution remains a separate deployment qualification.
These local checks do not establish real-cluster reliability, continuous history,
verified recovery or M4 acceptance.

### M2 overlapping-source receipt parity follow-up

CRD and Gatekeeper callbacks now use the same stored-writer replay decision with
history enabled or disabled. An unchanged object delivered by the other source
does not overwrite its stored receipt, adopt retired data or clear the original
writer's qualification. Missing/invalid incoming origin cannot erase a valid
receipt for that same member, identity, API version, UID and opaque version.
Fresh delivery by a new registration of the same source can refresh its receipt;
an actual UID/version or API-representation change can commit a new writer.
Equality checks do not order resourceVersions or establish version equivalence.

History-disabled versioned dynamic callbacks add one stored-resource point read
under the existing shared resource lock before deciding whether to write.
Receipt lookup failure stops that delivery's write/extraction and records the
existing persistence gap, without an unconditional-write fallback or automatic
retry. Cancellation after a context-ignoring lookup also stops the write. Both
modes continue edge extraction for a successful unchanged replay, allowing lost
relationships to be recovered without retagging the resource. Unversioned data
still cannot attest origin. No additional Kubernetes request, watch, background
worker, per-object side cache, GraphStore method or public field is introduced.

Fake-client integrations run both collector orders in named-member and standalone
scopes, with history on/off. They verify retirement, another source's restart,
same-source receipt refresh, replayed edge recovery, actual version changes,
foreign-member retention and metadata-only history. Portable receipt/failure
tests cover legacy and invalid origin, UID/API-version changes, failed reads and
writes, recovery on later delivery and cancellation. Deployed PostgreSQL cost
and real-cluster acceptance remain separate M4 work; local tests do not qualify
either.

### M2 core replay source-receipt preservation follow-up

Core collection can observe the same optional API endpoints as CRD collection,
including Kyverno policies. A new core informer or a changed local cache entry
with history disabled now consults the existing stored-writer receipt before
deciding whether to write. An unchanged member/identity/API-version/UID/version
observation preserves that writer, including registered, retired or replaced
private receipts and unowned legacy rows. Core delivery alone cannot adopt or
erase a dynamic registration for the same observed version. Actual UID/version
changes can still commit a new unqualified core observation; opaque versions
are compared for equality, never ordered across watches.

This reuses the existing `UpsertObservedResource` decision under the scope's
shared resource lock. A versioned cold or mismatched cache entry adds one point
read; unversioned writes keep their existing behavior. Successful stored replay
can populate the existing core cache without a resource write or resource-change
event. Unchanged cache hits continue to skip resource reads and writes while
still re-extracting edges. That fast path is not a fresh proof of the stored row
or a mechanism for repairing external storage deletion. No source-side cache,
Kubernetes request, worker, public field or GraphStore method is added.

Receipt lookup failure has no unconditional-write fallback and does not mark the
local version successful. Cancellation after a context-ignoring lookup stops
write/extraction and creates no false shutdown gap. Active receipt read/write
failures retain data and record the persistence gap. A later normal delivery can
commit the fresh observation, without clearing historical gaps. Notifications
and the legacy snapshot sink follow committed graph/resource changes; replayed
edges can recover after informer recreation without duplicate resource events.

Portable tests cover both scope/history modes, live/retired/replaced/unknown
source qualification, cold/repeated/recreated core delivery, exact point-read
counts, edge recovery, foreign-member preservation, fresh UID/version changes,
read/write failure, cancellation and later recovery. The existing 10K unchanged
resync regression remains required. Deployed point-read cost, real-cluster
behavior and M4 acceptance remain unqualified.

### M2 history-independent upsert identity qualification follow-up

Core, CRD and Gatekeeper upserts now apply the existing minimum observed-version
identity check after successful persistence while the callback is still active,
regardless of the history setting. A stored observation missing UID,
resourceVersion or apiVersion records a processing gap instead of leaving that
type qualified as initialized with no known gap. Such graph rows and their
existing derived-edge behavior remain retained; the row cannot attest a private
writer origin. This is minimum-field qualification, not a new parser for all
Kubernetes identity syntax or proof of complete observation.

An uncommitted write failure records the persistence gap. A dependency returning
after cancellation does not start extraction or add a new identity-processing
gap for shutdown, even if its in-flight resource write committed. Existing gaps
and committed valid-version history remain intact. Successful later delivery
does not clear earlier processing, persistence or extraction gaps.

Core's successful-version cache and legacy snapshot sink now require that same
minimum identity. A row missing apiVersion cannot poison the UID/version cache
and suppress a later complete delivery with the same UID/version. The incomplete
observation emits no qualified resource-history event. The shared history writer
also requires the previous row to have minimum identity before using it to
suppress duplicate history. A fresh complete observation following an incomplete
row can therefore record its first qualified version once; subsequent unchanged
replay remains deduplicated. Valid cross-version representation changes keep
their existing history semantics.

Portable callback matrices exercise all three pipelines, member/standalone
scopes, both history modes, complete and missing-field observations, active and
failed writes, cancellation before failure and cancellation with commit. They
verify source receipts, coverage classification, core cache eligibility, qualified
history, later identity/edge recovery and foreign-row retention. The change reuses
existing persistence and receipt reads; it adds no Kubernetes request, watch,
worker, storage capability, GraphStore method or public API field. Real-cluster
and deployed PostgreSQL behavior and M4 remain unqualified.

### M2 watch-deletion receipt parity follow-up

Core, CRD-instance and Gatekeeper object-delete callbacks now share conditional
deletion with history enabled or disabled. An old-UID tombstone cannot remove a
same-name replacement or its incident edges. An actual deletion notification
for the same member, namespace, kind, name, UID and API group may carry an older
opaque version or another representation: the atomic operation compares
the complete current stored receipt, including API version and private writer
origin. Concurrent UID/version/origin refresh makes that operation a no-op.
This does not infer version equivalence, metadata retirement or endpoint absence
as object deletion.

Missing callback UID/version/API-version fields retain data and record the
existing processing gap. Unverifiable stored identity and API-group mismatches
also retain data. Receipt read/conditional-write failures or
missing optional `ResourceVersionDeleter` capability retain data and record the
existing persistence gap; there is no unconditional delete fallback or automatic
retry. Source retirement still keeps data. Core callbacks forget only the
matching UID's successful-write marker, including when another collector already
removed the row; an old UID cannot invalidate a replacement's marker. Delete
broadcasts and configured metadata-only snapshot events follow committed
deletion only. Events use the committed stored version, not a delayed
tombstone's version; duplicate/uncommitted notifications emit no deletion event.
Cancellation before mutation prevents work, while cancellation after a confirmed
commit does not erase that committed event or notification.

Portable regressions cover all three callback paths, both history modes and
named-member/standalone scope, including stale/duplicate tombstones, older
representations, concurrent receipt refresh, missing capability, read/write
failure, cancellation, scoped retention and edge cascading. Fake fixtures carry
UID/version evidence, and store test adapters forward conditional deletion.
Short-mode discovery tests explicitly skip envtest control-plane startup. These
local tests do not establish deployed PostgreSQL behavior or M4 acceptance. No
Kubernetes mutation, extra watch, background worker, GraphStore v2 method or
public API field is added.

### M2 core callback cancellation and committed notification follow-up

Core upsert callbacks now recheck cancellation at entry, after acquiring the
scope/resource lock, after resource persistence and extraction, and before each
Secret-reference placeholder or edge write. Member removal and shutdown stop
subsequent callback stages when a dependency returns after cancellation. This
also applies to unchanged replay, which still re-extracts relationships while
active. No additional Kubernetes request, watch, cache or worker is introduced.

A dependency already in flight can ignore cancellation and commit. Keep that
resource/placeholder/edge and its confirmed resource receipt/history; do not
roll it back or infer deletion. An upsert callback notifies clients once if it
confirmed a resource, reference-placeholder or edge write, including when it
then exits on cancellation. An unchanged replay with only failed edge writes
does not send a false update notification; later confirmed edge recovery can
notify without manufacturing resource history. Notifications remain best effort.

Cancellation errors do not create persistence/extraction gaps or clear earlier
loss evidence. Active non-cancellation failures retain their existing gap
reporting, and successful partial extraction results still reach persistence.
Secret placeholders remain reference-only; this change does not watch/read
Secret objects or add Secret history payloads. Fake-client manager regressions
block all four stages, prove shutdown joins callbacks, and cover member and
standalone scopes, both history modes and replay. Portable failure tests cover
pre-cancelled entry, cancellation errors, sticky gaps, committed notifications
and active partial extraction. These offline tests do not qualify real-cluster
reliability, deployed PostgreSQL cancellation or M4 acceptance.

### M2 builtin extraction cancellation boundary follow-up

The shared builtin extractor registry now checks its parent context before each
extractor, after each dependency returns and before exposing the final batch.
Cancellation or an expired deadline returns the parent's context error and no
edges, including when an earlier extractor succeeded or failed with usable
partial results. Already-started dependencies may still return late; the
registry does not launch another extractor or expose their late output. Empty
registries also preserve parent cancellation rather than reporting success.

An extractor error under a still-active parent remains an ordinary partial
failure: keep its usable edges, run later extractors and return the first
error. This includes a dependency's own cancellation/deadline error when the
registry's parent remains active. There is no detached goroutine, global lock,
retry, new watch or additional query. Concurrent callers retain independent
contexts, so an old cancelled callback does not stop a healthy successor.

Core and Gatekeeper callbacks still keep resources, private writer receipts and
metadata-only history committed before cancellation. Cancelled registry output
cannot start Secret-reference placeholder or edge writes. Existing loss
evidence remains sticky; teardown does not create a new extraction/persistence
gap. Later active delivery re-extracts relationships without duplicating
unchanged resource history or adopting a new stored writer. The registry change
does not alter active partial-result persistence or the whole-batch scope guard.

Portable tests cover pre-cancelled and expired entry, every extractor position,
late success/error output, earlier partial failures, empty registries, real
builtin Service selection and phase-controlled concurrent callers. Core and
Gatekeeper callback regressions use the real registry across member/standalone
scope and both history modes, checking retained data, replay and scoped reads.
These checks do not establish real-cluster shutdown or deployed PostgreSQL/M4
acceptance.

### M2 shared Rego evaluation cancellation and failure handoff follow-up

The existing rule-evaluation cache now waits for a shared evaluation through a
completion channel or the waiting caller's own context. A cancelled waiter can
return while the first evaluator remains blocked; it does not cancel that
evaluator, remove its flight entry or disturb other members. Cancellation is
checked before returning cached results and after evaluation, so a dependency
returning after its caller was cancelled cannot publish that late value. The
actual evaluator still runs under the existing context/OPA timeout boundaries;
this change does not add a worker or forcibly interrupt context-ignoring code.

When an evaluator fails, active waiting calls return to the mutex-protected
ownership decision. Only one becomes the replacement evaluator for that key;
the rest wait for its completion or their own cancellation. A second cache check
under that mutex handles publication between the fast-path miss and ownership
selection. Failed values and partial values accompanying errors are not cached.
Each caller invokes its evaluation function at most once and retains its own
failure; no single call automatically retries its failed evaluation. Flight
entries are retired and waiters notified when the owning call exits.

The key uses member/API-version/UID/opaque version/rule hash plus the prepared
entrypoint (see the identity follow-ups below). Different member keys can
evaluate independently, and warm hits do not acquire the flight-map mutex.
Existing LRU capacity, metrics and per-evaluation timeout limits remain in
place. Cancellation without evaluation or a usable cache result adds no cache
hit/miss. No Kubernetes/storage query, watch, public API field or GraphStore
method is introduced.

Portable phase-controlled regressions cover cold/warm/waiting cancellation and
deadlines, failed and cancelled evaluators with one/eight waiters, sequential
failed handoffs, exact hit/miss counts, flight cleanup, member-key independence
and a real local Rego-engine warm-cache replay. Real-cluster shutdown behavior,
deployed load and M4 acceptance remain unqualified.

### M2 CRD Rego partial extraction follow-up

Resource-level Rego evaluation now returns edges from complete earlier modules
alongside the first active failure, including a missing compiled module,
evaluation error, recovered panic or malformed output. It still stops at that
failure, in the existing router order; it does not run later modules or retry
the failing module within that call. Decoding remains atomic within one module:
a valid prefix followed by a malformed edge contributes nothing from that module.
Successful whole-module values keep their existing cache entries, while failed
values remain uncached. A subsequent active delivery can recover missing edges.
Per-evaluation timeouts, cache capacity and other budgets are unchanged; the
query-identity follow-up below extends the cache key with the prepared entrypoint.

The CRD callback records an extraction gap for active failures and keeps usable
returned edges only after validating the complete batch in its explicit member
or unlabelled standalone scope. A foreign or empty endpoint rejects the entire
returned batch before any edge write, including a valid prefix. Edge write
failures retain separate persistence-gap evidence while other active writes can
proceed. Recovery does not clear prior gaps, adopt a different stored writer or
create duplicate metadata-only history for an unchanged source observation.
Existing retained resources/edges are not cleaned up.

Cancellation takes precedence over partial output. The engine checks it at entry,
between modules and after evaluation; cancelled calls return no edges. The CRD
callback rechecks it after evaluation and before each edge write. An already
started storage dependency can still commit despite cancellation; its committed
edge, source receipt and configured resource history remain without rollback.
Cancellation creates no new extraction/persistence gap and preserves earlier
loss evidence. This adds no worker, watch, Kubernetes/storage query, public API
field or GraphStore method.

Portable race regressions cover first/later module failures, atomic malformed
outputs, error causes, cached replay, cancellation and subsequent active recovery.
Callback matrices include the actual local Rego engine, named-member/standalone
scope, both history modes, whole-batch scope rejection, failed/cancelled writes,
committed receipts, metadata-only history and sticky gaps. These offline checks
do not establish deployed PostgreSQL behavior, real-cluster coverage or M4
acceptance.

### M2 Rego prepared-query cache identity follow-up

The existing bounded Rego cache now includes the prepared entrypoint alongside
member, full API version, UID, opaque resource version and source hash. A rule pack may register
identical source bytes with different entrypoints; those queries must have
separate entries and in-flight ownership. A source or entrypoint change selects
a new key without purging other cached results. Resource versions are compared
for equality and are not interpreted as numbers or ordered across observations.

Each module iteration captures one immutable compiled module under the engine
lock. Both the cache key and the cache-miss evaluation use that exact compiled
module. In particular, a call waiting for another evaluator does not look up the
module name again after a reload: it may complete with the older captured query,
while a subsequent call captures the replacement. The newer query can evaluate
independently of the older blocked key, and neither result is stored under the
other query's identity. This is a per-module snapshot, not an atomic rule-pack
reload or a guarantee that all modules in one resource call use one pack revision.

Existing timeout/recover guards, cancellation checks, error metrics, successful
whole-module caching and active partial-result semantics remain in place. Failed
compilation leaves the prior compiled module and usable cache entries intact.
LRU capacity and evaluation budgets are unchanged; no worker, watch, storage read,
public API field, GraphStore method or rule-pack format change is introduced.

Portable actual-engine regressions cover identical-source entrypoints in both
registration orders, named-member/standalone scope, cold/warm replay and a
phase-controlled failed-leader handoff across source-only or entrypoint-only
reload. They verify independent new-query progress, old-query completion,
restoration without cache poisoning, failed compilation and exact hit/miss counts.
Real-cluster behavior, deployed load and M4 acceptance remain unqualified.

### M2 Rego cache eligibility for incomplete observations follow-up

Resource-level Rego evaluation now requires the existing
`graph.ValidResourceVersion` predicate before using the result cache. The resource
must have kind, name, API version, UID and opaque resource version, and must not
be a reference-only Secret. Missing identity cannot prove equivalence between
deliveries or objects. An ineligible observation still evaluates its routed
modules, but neither reads nor populates the LRU or the shared in-flight map.
It does not wait for an unprovable shared key or change cache hit/miss counters.
Eligible observations keep the existing bounded cache and query-identity keys;
the engine does not invent UID/version fields or use payload hashes as receipts.

Both paths execute the same captured compiled query through the existing timeout
and panic guards, decode each module atomically and recheck cancellation. Active
failure still returns whole earlier successful modules alongside the error;
cancellation discards the call's output. Timeout/panic counters remain available
for uncached evaluation. Cache capacity, per-module evaluation budgets and
member/standalone edge validation remain unchanged. Incomplete repeated delivery
can therefore cost another bounded evaluation, rather than reuse an unqualified
result. This adds no worker, watch, store query, public API field or GraphStore
method.

CRD callbacks retain fresh derived relationships alongside the existing sticky
processing gap for incomplete persisted identity. They do not fabricate private
origin receipts or metadata-only history for those observations. A later complete
delivery restores qualified receipts, history eligibility and normal caching;
unchanged complete replay adds no duplicate resource history. It cannot erase
the earlier gap or infer deletion of retained edges. The offline CLI keeps its
existing fixture UID mapping for rule input; samples without resourceVersion
also follow this uncached evaluation path.

Portable actual-engine tests cover missing UID/version/API version, changed
payloads and separate objects, qualified cache preservation/recovery, an already
blocked unprovable key, cancellation, active errors, timeout, panic, malformed
output and partial-module results. Real-engine CRD callback matrices cover both
scope/history modes, fresh edge retention, sticky processing gaps, private source
receipts, metadata-only history and foreign-member isolation. These checks do not
qualify real-cluster behavior, deployed cost or M4 acceptance.

### M2 Rego cache separation across API representations follow-up

The result-cache key now includes the resource's full `GroupVersion` API-version
string, including its API group. A resource may arrive through two served API
versions with equal UID/resourceVersion while rule input differs, including the
`apiVersion` field and converted payload shape. UID/version equality alone does
not establish equivalence between those representations. Each full API version
therefore has independent cached results and in-flight ownership for the same
member/object/source/entrypoint. A blocked evaluation for one version does not
hold up a call for another version, and unchanged replay within a version still
uses the cache.

Rule routing remains group/kind based; wildcard matching and rule-pack formats
are unchanged. Both raw informer input and the minimal resource-input fallback
use the same representation boundary. Incomplete identity still bypasses caching.
The LRU keeps its existing entry limit, so different API versions occupy separate
entries within that limit rather than increasing capacity. Timeout, cancellation,
panic/error guards and partial-result semantics remain unchanged. No watch,
worker, Kubernetes/storage query, public API field or GraphStore method is added.

CRD endpoint-switch callbacks continue to apply the existing stored-writer
rules: fresh delivery through another API version can update the current graph
representation and private source receipt, while equal qualified UID/version
does not manufacture another metadata-only history event. Retirement alone
still authorizes neither adoption nor deletion. Existing relationships remain
retained; a new representation can add its own successfully derived relationships.
Cache separation does not prove version equivalence, choose an authoritative
representation or change those persistence/history policies.

Portable actual-engine regressions cover both delivery orders, raw and minimal
input, exact and wildcard routing, member/standalone scopes, independent progress
past another representation's blocked key, full API-group separation and exact
cold/warm cache accounting. CRD callback matrices cover endpoint retirement and
fresh version delivery in both history modes, new scoped relationships, matching
source receipts and unchanged metadata-only history counts. Real-cluster watch
behavior, conversion correctness, deployed cost and M4 acceptance remain pending.

### M2 collector read and edge scope follow-up

Core and Gatekeeper extractors now receive a read-only scope adapter rather than
the complete store. Every list uses the existing `ListResourcesInCluster`
capability with the owning member, regardless of an omitted or conflicting
extractor filter. Empty scope explicitly selects unlabelled standalone rows,
not all members. Kind, namespace and label filters remain intact. A cancelled
or failed read, or a store response containing another member, exposes no
partial results and has no unscoped-query fallback or automatic retry. The
adapter does not expose GraphStore mutation/query capabilities beyond its one
filtered-list method; it is not a sandbox for arbitrary extension code.

All three collector paths validate the entire edge batch in that explicit scope
before any edge or core Secret-reference placeholder is persisted. Relative
endpoints are qualified for a named member; foreign prefixes and empty endpoints
reject the batch, including a standalone callback that returns a named-member
endpoint. External identity colons after the first slash remain valid. Rejection
keeps the committed source resource/history and records an extraction gap; it
does not persist a valid prefix of an invalid batch. Valid active partial
extraction keeps its existing behavior. The older `ScopeEdgesToCluster` helper
retains its empty-scope pass-through behavior for callers outside these explicit
collector paths. Existing stored data is not cleaned or retroactively reowned.

Portable tests exercise broad/conflicting extractor filters, opaque read-only
capabilities, exact filter preservation, cancelled/failed/foreign-row reads and
whole-batch rejection before Secret placeholders. Builtin Service selection
excludes other members in standalone mode, and builtin Kyverno report lookup
cannot apply another member's results to local edges. A fresh local report can
still repair edge results on unchanged replay without adding resource history.
CRD/Gatekeeper fake-client tests cover both named-member and standalone scopes.
No Kubernetes request, additional storage query, worker, public API field or
GraphStore v2 method is added. Real-cluster and deployed PostgreSQL qualification
remain separate M4 work; these tests do not establish complete collector coverage.

### M2 Kyverno report-read failure qualification follow-up

Kyverno extraction distinguishes a successful empty report query from a failed
store read. Missing `PolicyReport` / `ClusterPolicyReport` rows still permit
plain ENFORCES edges with no result attribute. A failed query contributes no
rows, including any partial response returned with its error. The other report
kind can still supply usable observations; the extractor returns the first
report-read error alongside its valid edges. Core collection records the existing
sticky extraction gap rather than silently treating that read as successful.

If a later target query fails, earlier successful target matches remain available
alongside the error. The failed query's partial rows are excluded. Other
registered extractors continue under the registry's existing partial-result
contract. Each existing lookup runs at most once per callback; no storage query,
worker, automatic retry, public response field or Kubernetes permission is added.
Explicit member/standalone read and edge scope guards remain in force.

Cancellation checks before and after report/target reads stop subsequent lookups,
including when a dependency returns rows after cancellation. The collector keeps
already committed resource/history writes, skips derived writes and preserves
prior gaps without fabricating a new extraction failure for shutdown. A normal
later delivery can repair the report overlay without duplicate resource history;
successful replay does not erase the earlier extraction gap. Stored graph data
is not cleaned or treated as deletion evidence.

Portable extractor and core-callback regressions cover failed/absent reports,
partial responses, usable edges after later target failure, registry continuation,
foreign-report isolation, both history modes, cancellation and sticky-gap replay.
These local checks do not qualify actual Kyverno installation, complete policy
coverage, deployed PostgreSQL behavior or M4 acceptance.

### M2 Gatekeeper partial-extraction retention follow-up

Gatekeeper target extraction now returns earlier successful matches when a later
kind or namespace query fails. Rows accompanying the failed query are excluded;
confirmed violation attributes on earlier edges are retained. The registry can
still contribute other extractors' results under its existing partial-result
contract. Queries stop at the first target-read failure without an automatic
retry. Context checks before and after each target query stop further reads
when a dependency returns after cancellation.

The Gatekeeper collector records an extraction gap and continues with usable
partial results. It validates the complete returned edge batch before any edge
write, even when extraction returned an error. A foreign or empty endpoint
rejects the entire batch; relative endpoints remain qualified to the owning
member, or explicitly unlabelled standalone scope. A failed edge write records
the separate persistence gap and does not suppress later valid writes. This
uses the existing scoped lister, resource lock, history writer and graph methods;
it adds no query, watch, worker, permission or public API field.

Cancellation after extraction skips all derived writes and adds no false
extraction failure for shutdown. If cancellation occurs during an edge write,
that write can still commit, but later writes stop. Already recorded gaps and
committed resource/history receipts are retained. A later normal delivery can
repair missing edges without duplicating unchanged resource history or clearing
the earlier extraction, processing or persistence gaps. No stored graph data is
removed or treated as deletion evidence.

Portable regressions cover later kind/namespace query failures, failed-query
partial rows, registry continuation, whole-batch scope rejection, violation
attributes, edge-write failures, cancellation, replay and private source receipts
in member/standalone scopes with both history modes. Real Gatekeeper installation,
deployed PostgreSQL behavior, complete policy coverage and M4 remain unqualified.

### M2 NetworkPolicy partial-extraction retention follow-up

NetworkPolicy ingress and egress extraction now retain successful peer edges
when a later peer, rule or namespace lookup fails. This includes earlier
namespaces inside one combined pod/namespace selector and earlier peers outside
that selector. Rows returned with a failed query do not contribute edges. The
existing registry retains independent SELECTS_NP and other extractor results;
the core collector records the existing sticky extraction gap before persisting
valid scoped partial results. A successful empty namespace query is not a read
failure and still permits independent selection edges.

All NetworkPolicy target reads check cancellation before and after the existing
query, including SELECTS_NP, pod-only peers, namespace-only peers and combined
selectors. A context-ignoring dependency cannot cause another query to begin
after cancellation or contribute late rows. The callback retains a resource or
history write already committed before cancellation, skips derived writes and
preserves prior gaps without adding a false extraction failure for shutdown.

The existing member/explicitly unlabelled standalone lister and whole-batch edge
scope guard remain in force. A normal later delivery can recover missing edges
without another unchanged-resource history event or clearing the earlier gap.
No query, watch, worker, permission, public field or GraphStore method is added;
stored data is not cleaned or treated as deletion evidence.

Portable regressions cover both directions, later peer/rule/namespace failures,
failed-query partial rows, query counts, cancellation at each read stage,
independent registry output, member isolation, both history modes, empty queries,
committed notifications and replay recovery. These edges remain declarations,
not effective CNI reachability. Existing matchLabels-only selector support and
the policyTypes, wildcard/empty-peer and ipBlock limits remain unchanged.
Real-cluster behavior, deployed PostgreSQL qualification and M4 remain pending.

### M2 existing snapshot-route member visibility follow-up

Existing snapshot list/diff handlers now authorize before history reads and use
explicit optional scoped capabilities. V1 federation requests select one attached
`cluster`; standalone scope reads only unlabelled rows, with unknown legacy
provenance. The frozen v1alpha1 query/response schemas gain no fields. Its
unscoped history aliases reject federation/restricted access instead of bypassing
the v1 selector. An authenticated forbidden scope is rejected before attachment
lookup; denied requests never read storage or disclose hidden attachment names.

`HistoryEventReader` supplies bounded metadata for diff classification. A new
optional `HistoryMarkerReader` supplies exact-member marker lists in both stores
without changing GraphStore v2. Filtering occurs before limits/counts. Memory
reads its bounded test-support ring; PostgreSQL applies cluster equality and a
1001-row lookahead in a read-only transaction with a five-second statement
limit. The shared marker builder caps returned rows and metadata. PostgreSQL
container tests are provided but are not established by short-mode local runs;
no indexed scan-performance claim is made.

Handlers reject truncated event pages, row/byte overflow, foreign metadata and
out-of-order event projections rather than returning misleading partial diffs.
They revalidate explicit adapters and emit closed provider-error text. Shared
classification preserves the existing response shape and uses only authorized
metadata. Final response encoding is capped at 2 MiB, with a five-second deadline
and the existing shared two-request analysis admission budget. Cancelled/late
workers cannot write success and retain admission until completion. Bodies are
not cached and vary by Authorization.

Portable route/store tests cover exact scope including empty legacy scope,
same-UID events across members, denied reads before attachment/storage access,
alpha alias rejection, explicit capability wrappers, Secret exclusion, scoped
counts, overflow, cancellation, late workers and admission sharing. OpenAPI adds
the v1 selector only. These checks do not qualify durable deployment, continuous
history, recoverable snapshots, scoped internal marker creation, snapshot Web/CLI
member selection or M4 acceptance. Detached retained scopes require reattachment
before this read API can query them. See the [multi-cluster history contract](../installation/multicluster.md#scoped-history-reads-v17-development).

### Existing history-client member selection follow-up

The snapshot page, topology decoration and change-log panel now derive history
scope from the existing cluster selection and federation mode. They disable reads
for the all-members view, detached selections, unavailable mode evidence and
cluster-list failure. Empty configured federations remain explicitly federated;
only confirmed standalone mode selects unlabelled history. No effect copies
derived scope into additional state. Query keys include the selected member,
abort signals flow to transport, and late results cannot replace a different
member's query. Loading/error states hide cached rows and reset old decorations.
Full resource IDs include member prefixes and the actual empty namespace;
aggregated nodes are never decorated as resource instances. Member rows remain
plain text until member-aware resource-detail navigation is available.

History requests do not retry or fall back to global reads. Marker polling uses
the existing one-minute interval only while reads succeed. Server error text is
shown for authorization/budget/capacity/deadline failures; 503 no longer falsely
means that history is necessarily disabled. Credentials continue through the
existing same-origin transport, with no token UI or persistence.

The main CLI previously had only `snapshot trigger`. New read-only `snapshot list`
and `snapshot diff` commands use the bounded `historyclient` package, one GET and
explicit `--cluster`; they reject repeated/empty/multiple scope flags, redirects,
unknown metadata fields, unexpected content types, oversized bodies and invalid
list counts/scopes. Bearer credentials come only from `KUBEATLAS_TOKEN`, remote
HTTP is rejected, and provider error bodies/URLs are not echoed. Typed metadata
is validated and reserialized for stdout, so this is distinct from the exact-wire
impact capture/export contract. The diff schema has no member echo: its scope
relies on the requested URL and the trusted server, not a fabricated guarantee.
The internal trigger write and kubectl-plugin commands are unchanged.

Portable checks exercise real API/client integration, CLI scope/auth/error
handling, Web cache and delayed-response isolation, member/cluster-scoped canvas
identity, blocked scopes and unavailable/error states. Browser QA uses the
production build with synthetic API/WS transport at desktop and mobile sizes;
it does not establish real-cluster behavior, PostgreSQL deployment performance,
member-specific marker creation, complete retained history or M4 acceptance.

### M2 internal member marker creation follow-up

`POST /api/_internal/snapshot/trigger` now requires snapshots to be enabled and
uses the same exact-member visibility/attachment checks as the history reads.
Federation requires one member, standalone selects only unlabelled graph rows,
and no all-member fan-out or global fallback occurs. Visibility is not write
permission: deployment/network controls must continue to restrict this internal
operator surface. No public write UI, Kubernetes write, or new permission grant
is introduced.

The optional `SnapshotMarkerRecorder` preserves the GraphStore v2 method set.
Memory counts and appends under one cancellable write lock. PostgreSQL counts
and inserts with one parameterized `INSERT ... SELECT` statement, observing one
MVCC snapshot. The edge join requires both stored endpoint resources in the
selected member, excluding dangling and cross-member tuples. Counts include
reference-only graph nodes and do not materialize or return graph payloads. The
handler echoes the stored receipt's counts and trigger, plus an optional named
`clusterId`, and rejects invalid or foreign receipts. Existing memory marker-ring
retention and legacy append semantics are unchanged.

Recording shares the two-request analysis admission, uses a cooperative
five-second context, and remains synchronous so there is no abandoned mutation
worker. PostgreSQL also sets a five-second statement timeout; rollback cleanup
can take up to another second. This is not hard preemption of a faulty provider.
Errors are closed; expiry or a commit error can leave an unknown write outcome.
There is no implicit retry or idempotency guarantee, and operator repetition can
create additional markers. A successful marker does not drain the writer,
repair missing events, establish complete history, or qualify recovery.

Portable tests cover member/empty-scope counts, dangling and cross-member edges,
parameterized metacharacter scopes, persisted receipts, concurrent count
consistency, cancellation without delayed writes, disabled/unsupported stores,
authorization before attachment/storage, invalid queries, late/foreign receipts,
closed errors, no retries and shared admission. The PostgreSQL contract test is
registered but skipped by `-short`; source compilation does not establish its
runtime SQL behavior or deployed performance. V1 OpenAPI gains the selector and
optional member echo; the frozen alpha specification stays unchanged.

The trigger-client follow-up below adds member selection and an existing-Secret
credential reference to the CLI/CronJob path. Actual PostgreSQL execution and M4
cluster acceptance remain pending. No real cluster is required for this local
source/test slice.

At marker-creation commit `cc389e7`, local graph/store/API/history-client short
race suites, changed-code lint, vet, CGO-free build, frozen-alpha compatibility
and docs build passed. The expanded `go test -short -race -p 1 ./...` gate was
**not green**: `TestMemberFactoryBindsRecordingAndJoinsOnlyOwnPendingWrites`
blocked west delivery (also reproduced with a pre-change source overlay), and
`TestMemberFactoryDynamicHistoryOverlapRepairAndReattachment` returned ten east
events instead of eight. Twenty pre-change focused runs did not reproduce that
count mismatch, so its cause was unresolved at that checkpoint. Full lint also
reported `QF1001` in `impact_availability.go`. The follow-up below separates
those historical results from the current regression contracts.

### Member-history regression contracts

The member-recording lifecycle fixture formerly blocked **every** east event.
Once CRD and Gatekeeper delivery joined core delivery, two east events could
occupy both shared writer workers. Per-member recording tokens isolate lifecycle,
pending/loss evidence and draining; they do not reserve a worker or guarantee
fair scheduling. A controlled pool-saturation test now holds both workers in
east, verifies west remains queued/pending, then releases capacity and verifies
both members drain without loss. The production-factory lifecycle test instead
blocks only the east Pod event, confirms east dynamic events and the west Pod
commit, and preserves its pending-write, independent removal and same-name
exclusion assertions. It stages west startup after east's dynamic deliveries
commit, keeping the initial burst within the existing four-slot queue rather
than relying on worker startup scheduling. Worker count, queue size and retry
budgets are unchanged.

The overlap fixture formerly advanced to deletion after observing two committed
update events. A committed event acknowledges only the first successful source;
core, generic CRD and Gatekeeper callbacks are independent. A deterministic
history test now delivers each watch's update/delete pair in source order while
interleaving the watches. After a committed delete removes the graph receipt,
a late upsert can reinsert the same UID and produce another metadata observation;
its later delete removes it again. One late watch adds two events. This explains
a mechanism consistent with the earlier extra update/delete pair, but the old
log has no source attribution and does not identify which callback was late.

Test-only extraction probes now acknowledge each source's initial and update
handling before the overlap fixture advances to the next phase. Exact event
counts and duplicate/payload assertions remain. Reattachment clears only the
probe observations, waits for a fresh core callback and initial empty Constraint
CRD delivery, and verifies the retired Foo CRD does not resume or mutate its
retained graph row. No production ordering barrier, tombstone cache, watch,
Kubernetes request or new history guarantee is introduced. ResourceVersions
remain opaque; delayed cross-watch delivery can temporarily restore a deleted
node and add observations. This limitation is now explicit in the recorder's
contract and is not fixed by making the controlled fixture deterministic.

Both formerly intermittent factory cases and both new controlled regression
cases passed twenty short/race repetitions. The availability-validator lint
expression now uses the equivalent `!active && !memberRetention` form. These
portable checks do not qualify real-cluster behavior, PostgreSQL deployment,
continuous history or recovery.

The final expanded short/race suite passed. First-party lint
(`./cmd/... ./pkg/... ./tools/...`), `go vet ./...`, CGO-free verification,
alpha compatibility and docs checks passed. CLI diagnostic-write results are
now explicitly best-effort, preserving the existing failure exit status and
single-request behavior. The unrestricted `golangci-lint run ./...` still reports
two `govet inline` suggestions in `web/node_modules/flatted/golang/pkg/flatted`;
those third-party files and the lint configuration were not changed.

### Internal trigger CLI and single-member scheduling follow-up

The main CLI's trigger path now uses `historyclient.Trigger`: one POST with
explicit member/trigger scope, optional environment bearer credential, normal TLS
trust, no environment proxy, no redirect/retry/fallback and a thirty-second
ceiling. A fresh non-reusing transport prevents replay on a reused connection.
Responses have a 4 KiB body and 64 KiB header cap. Typed validation requires all
count/duration fields, nonnegative integers, the selected member and trigger;
unknown/duplicate keys (including escaped duplicate names), null required
fields, malformed/trailing/oversized data and interrupted receipt delivery are
rejected. Provider bodies and credential-bearing URLs are never echoed. Only
validated typed metadata is serialized to stdout; stdout failure remains an
unknown acknowledgement outcome and cannot start another write.

The CLI rejects repeated flags and invalid selection before dispatch. Help and
parse errors do not print inherited or supplied credential URLs. HTTP is limited
to literal loopback or exact `service.namespace.svc` / fully qualified
`.svc.cluster.local` hostnames for the existing internal CronJob path. That
syntactic exception does not establish pod, DNS or network trust; operators must
trust the plaintext internal path before attaching credentials. Other remote
addresses require HTTPS. The default is literal loopback, not localhost DNS.

The chart can pass one `snapshots.trigger.cluster` and inject `KUBEATLAS_TOKEN`
through an existing Secret's name/key. It neither renders plaintext token data
nor adds Kubernetes permissions. Named standalone selection, partial references,
and a selected member with visibility rules but no credential reference are
rejected. Empty defaults preserve the standalone command; v1.7 federation still
rejects an omitted member at runtime. No cluster union or per-member CronJob
fan-out is added. New flags require a matching development image; the chart's
v1.6.0 metadata/default image and all release artifacts are unchanged.

The existing schedule, `Forbid` concurrency, 120-second deadline, Pod resources,
NetworkPolicy source labels and disabled service-account token remain. Job
`backoffLimit` is zero with `restartPolicy: Never`: failures do not trigger a
replacement attempt through Job backoff. Later schedule ticks are distinct
observations, not retries of an unknown commit. Kubernetes Job execution is not
an exactly-once guarantee; controller duplicates or operator repetition can
produce extra markers. History completeness, drained delivery, backups and
recovery remain unqualified.

Portable checks cover actual scoped API/client integration, CLI auth/validation
and outcome codes, malformed/foreign/duplicate/truncated receipts, cancellation,
redirect non-following, no retries, Secret-reference rendering and no-fan-out
CronJob budgets. These are local source/loopback/rendering checks; no real
cluster, PostgreSQL instance, image build/download or published artifact is
established by this slice.

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

### M3 Web/CLI integration {/* #m3-source-and-synthetic-delivery-review */}

The development Web UI and CLI consume the v1 impact API for scoped counts,
representative paths, reference evidence, and observation limits. JSON/HTML
exports retain the captured result. These capabilities supersede the historical
pending client/export notes in the individual slices above. They are available
in development source; v1.7 remains unreleased and candidate acceptance is pending.

| Capability | Behavior and limits |
| --- | --- |
| Server-backed analysis | Select a concrete resource in its cluster to inspect dependents or dependencies, server counts and representative paths. Canvas filters do not narrow the analysis scope. |
| Captured exports | Web JSON/HTML downloads require sensitive-topology consent and make no extra analysis query. Query changes reset consent. CLI reports use private, non-overwriting files. HTML has no active/external elements and retains its restrictive CSP. |
| API observation details | Expand the Web section to inspect before/after inventory state, check time and stale/stopped/limited flags, optional API presence/version/shape assessments, and advertised endpoints without captured registration evidence. It reads the existing capture without another request. |
| Identity and cluster boundaries | UID conflicts and denied queries hide results and exports. Cluster switches clear old results, including late responses. Authorization scope is applied before graph/evidence reads; deployed external authentication still requires candidate validation. |
| Counts, paths and limitations | Ordinary and authorization counts remain separate. Secret targets are reference-only; truncation and incomplete empty results stay qualified. Inventory enumeration is not proof of successful watches, effective permissions or complete graph observation. Empty endpoint lists do not establish complete collector coverage. |

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
failure qualification on Tier 1 and Tier 2. Frozen-candidate deployment validation
remains pending. API inventory presentation does not close complete collector
coverage, version equivalence, or
per-member history qualification. Active member endpoints have bounded
missed-delete repair as described above. Federated members run their own bounded inventory, CRD and
Gatekeeper workers as described above; failed, denied, stale, unsupported or
limited evidence remains qualified and cannot prove absence.

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

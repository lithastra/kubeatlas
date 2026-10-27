---
sidebar_position: 5
title: Roadmap
---

# Roadmap

This is a rough, **non-binding** plan for where KubeAtlas is going.
Dates slip, scopes change, and external feedback frequently reshapes
priorities. Treat this page as direction, not contract.

For the current state, see [What is KubeAtlas](./).

## Where we are

**v1.6.0 was released on September 26, 2026.** Its operability signals,
runbooks, exact-source platform preflight, and public OCI artifact audits are
documented in the [release matrix](./release-process.md). Validation was
narrowed to one-hour functional observation plus a separate public-artifact
smoke check; dedicated performance and endurance acceptance are not claimed.
The original v1.6 planning targets below are retained as history, not a report
that every target passed. New installations should use v1.6.0.

**v1.5.2 is released.** It closes the Secret data boundary:
KubeAtlas retains reference-only dependency nodes without collecting Secret
objects or persisting their values and identifying metadata. Existing Tier 2
rows and event payloads are scrubbed during migration. The exact-main release
preflight, protected-branch Tier 2 and snapshot gates, public artifacts, rule
pack audit, GitHub Action, and Krew manifest have all been verified. Optional
catalog integrations continue on their own review cadence.

**v1.5.1 focuses on release reliability.** CloudNativePG 0.22.1
becomes an explicit cluster-scoped prerequisite,
embedded database storage is retained on uninstall by default, and CI
verifies the v1.5.0 upgrade, database-recovery, uninstall-retention,
and snapshot paths.

**v1.5.0 is out.** Earlier releases shipped v1.1 (rule packs and
plugins), v1.2 (offline rendering), v1.3 (multi-cluster
federation, platform-identity edges, cartography UI), and **v1.4**
(offline diagnostics, Gatekeeper/Kyverno policy visibility, opt-in
anonymous telemetry, and v1alpha1 usage tracking that will inform a
future decision on retiring v1alpha1). **v1.5** is a non-breaking
minor: an opt-in OpenTelemetry runtime overlay (`CALLS_AT_RUNTIME`),
read-side multi-cluster RBAC visibility (F-206), and an internal
GraphStore v2 clean-up that surfaces `graphstore_version` on
`/api/v1/info`. Install with
`helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas --version 1.6.0`
— see the [Quick Start](./quick-start.md).

| Milestone | Status | What it delivered |
|---|---|---|
| **Foundation** | ✅ Done | CLI binary, in-memory graph, 8 edge types, 16 watched resources, contract-tested store interface, contributor docs, CI gates. No API, no UI, no Helm Chart. |
| **v0.1.0** (MVP) | ✅ Released | REST + WebSocket API, React/MUI Web UI with Cytoscape topology and Mermaid neighbour view, Helm Chart with secure defaults, Playwright E2E, multi-platform release. Available as `oci://ghcr.io/lithastra/charts/kubeatlas:0.1.0`. |
| **v1.0** | ✅ Released | Tier 2 persistence (PostgreSQL + Apache AGE), Rego rule packs, RBAC graph, blast radius, orphan + cycle detection, `/api/v1/*` GA, cert-manager TLS, OpenShift detector + embedded pack, chaos test suite. Available as `oci://ghcr.io/lithastra/charts/kubeatlas:1.0.0`. |
| **v1.1 / v1.2 / v1.3** | ✅ Released | Cloud rule packs, snapshots, search, plugins (v1.1). Offline `kubectl atlas`, graph-image export (v1.2). Multi-cluster federation, platform-identity edges, HPA support, GitHub Action, cartography Web UI redesign (v1.3). |
| **v1.4 / v1.5** | ✅ Released | Offline diagnostic report, Gatekeeper/Kyverno policy visibility, opt-in anonymous telemetry, v1alpha1 usage counters (v1.4). v1.5 (a non-breaking minor): OpenTelemetry runtime overlay (`CALLS_AT_RUNTIME`), read-side multi-cluster RBAC visibility (F-206), an internal GraphStore v2 clean-up, and the Backstage plugin reaching GA at Headlamp parity. `v1alpha1` stays frozen — there is no v2.0 on the committed roadmap. |
| **v1.5.1** | ✅ Released | Explicit CloudNativePG prerequisite, retained Tier 2 data by default, reproducible PostgreSQL + AGE image, and enforced upgrade, recovery, uninstall-retention, and snapshot release evidence. |
| **v1.5.2** | ✅ Released | Secret relationships remain reference-only while Secret objects, values, identifying metadata, and historical event payloads are excluded or scrubbed. |
| **v1.6.0** | ✅ Released | Production-operability signals and runbooks, source platform/upgrade/restore validation, and verified public OCI artifacts. One-hour functional scope; no endurance or dedicated performance acceptance claim. |
| **v1.7.0** | In development; Web/CLI integration available | Server-backed impact analysis provides bounded paths, reference explanations, separate RBAC associations, observation continuity, and cached availability. Web and CLI exports retain the captured result. The Web UI also shows before/after API inventory, optional API assessments, and advertised endpoints without captured observation. Frozen deployment integration and M4 acceptance remain pending; v1.7 is not released. |
| **Further out** | 💭 Sketch | Cloud-resource integration, third-party platform deep-dives, federation cross-cluster edge inference; a possible future `v1alpha1` retirement (which would version a v2.0). |

## v1.7.0 — explainable impact analysis (in development)

The accepted direction is one bounded DevOps workflow: select a resource,
inspect its observed dependents and relationship paths, understand missing or
stale data, and export the same result for review. It is not an outage predictor
or an arbitrary manifest simulator. The [M0 contract and acceptance catalog](./development/impact-analysis.md)
define semantics, API direction, limits, and implementation stages.

The four work packages are shared analysis semantics, path explanations,
observation-coverage warnings, and consistent Web/CLI/JSON/HTML presentation.
Existing v1alpha1 responses remain frozen; new analysis uses an additive v1
surface. Single-replica operation, external authentication, read-only Kubernetes
access, and the reference-only Secret boundary remain unchanged.

Implementation proceeds through M0 contract, M1 core, M2 observation/API,
M3 clients/reports, and M4 candidate acceptance. M1 currently implements the
internal core, consistent memory/PostgreSQL projections, reference evidence,
and a separate bounded RBAC association facet. Reference explanation now also
qualifies conflicting modeled volume sources, projected-source alternatives and
nonempty subPath/subPathExpr combinations as unknown. The same rule now applies
to an envFrom entry with both ConfigMap/Secret sources or a valueFrom entry with
both modeled key sources. Separate environment entries retain their own optional
flags and container-restart classification. Existing relationships and
counts remain unchanged; later valid source versions restore explanation.
Portable regressions cover seven builtin consumers, both traversal directions,
member/standalone scope and memory projection parity. This is validation of
retained reference fields, not full Kubernetes schema or deployment acceptance.
Ingress collection now also derives ROUTES_TO from defaultBackend Service
references, deduplicated with rule backends. Same-version delivery fills the
previously missing edge without rewriting the resource or duplicating history;
failed edge writes retain gap evidence and later replay can recover. Missing
Service targets remain unresolved rather than synthesized, with local scoped
forward/reverse impact regressions. This does not establish effective routing.
HTTPRoute parent extraction now also checks the declared API group before
building ATTACHED_TO edges. Omitted group defaults to gateway.networking.k8s.io;
explicit core/other groups cannot bind a same-name modeled Gateway. Malformed
group values preserve the confirmed parent prefix and independent backend edges
with an extraction gap. Local scope/history/replay regressions cover missing
targets, failed writes and forward/reverse recovery without clearing known gaps.
Existing stored edges remain retained; allowed attachment and traffic are unverified.
HTTPRoute core Service backends now also require omitted or empty group. Other
explicit groups cannot bind same-name core Services, and malformed group values
retain earlier backends and independent parent extraction with a gap. Portable
parent/backend regressions cover scope/history modes, deduplication, failed
writes, missing targets and forward/reverse recovery. Legacy custom-kind backend
modeling remains unchanged; general group-qualified identity is still unverified.
ConfigMap collection now also includes init/ephemeral container environment
references and projected volume sources, with deduplication across all reference
locations. Same-version replay fills the missing relationships without another
resource write/history event. Local regressions cover seven builtin sources,
member/standalone scope, both history modes, failed writes, unresolved targets
and version-bound forward/reverse reference explanations without payload reads.
Label equality now requires the selected key to exist, including when its value
is empty. Service and NetworkPolicy extraction no longer treat an absent key as
an empty value. Memory list, count and namespace-subgraph filters follow the same
key-presence rule as PostgreSQL's existing JSONB containment queries. Empty
Service selectors still select nothing; empty NetworkPolicy matchLabels and
store filters keep their existing match-all behavior. Portable regressions cover
these distinctions, both scopes, replay/history and scoped impact paths. Stored
relationships from earlier observations remain retained; this change does not
prove deletion ownership or reconcile old edges, and adds no selector operators.
Service selectors and NetworkPolicy matchLabels now also reject malformed
non-null objects and non-string values before target reads, instead of dropping
conditions or broadening them into empty selectors. Earlier valid peers and
independent extractor results remain usable with an extraction gap; a later
valid source version recovers collection without clearing prior loss evidence.
Omitted/null fields retain their existing behavior. Local scope/history/replay
regressions add no watches, selector operators or retained-edge cleanup.
The same shape guard now covers Gatekeeper labelSelector and Kyverno resource
selectors. Malformed policy conditions cannot become broader ENFORCES queries.
Kyverno retains validated earlier descriptors/rules and report-read errors, then
stops at the malformed descriptor. Local callback regressions preserve source
receipts, Gatekeeper origin ownership, violation/report attributes, both scope
and history modes, and sticky gaps during valid-version recovery. ENFORCES
remains excluded from ordinary impact paths; this adds no admission evaluation.
Policy namespace lists now also reject malformed non-null values, non-string
entries and empty-string entries as a whole. Invalid input cannot fall back to
unrestricted namespace queries or silently lose conditions. Omitted/null/empty
lists retain their existing meaning, and namespaced Kyverno Policy scope stays
local. Portable regressions cover validated prefixes, error joining, cancellation,
source/history receipts and recovery without clearing gaps or deleting old data.
M2 records per-resource-type
initial delivery, list/watch failures, and processing/write gaps, and now
connects that evidence to internal analysis with before/after continuity checks.
Requirements are independent of query matches; dynamic source scope remains
explicitly unverified instead of claiming complete ordinary coverage.
Cached storage health and history-writer status are now separate from current
graph success. New informer history now preserves its cluster identity, with an
optional bounded metadata-only event reader shared by memory and PostgreSQL.
Standalone Tier 2 history now checks retained event bounds and marker existence
in a bounded background read; analysis consumes the expiring cache. These
metadata observations do not establish continuous history or a recoverable
snapshot. Tier 2 members with history enabled now have independent expiring
retention-metadata caches owned by their member bundles. Scoped background reads
and cache-only impact queries preserve member boundaries. Member core-history
sinks now carry their own recording lifecycle, pending delivery and sticky
queue/write-loss evidence through the existing shared writer budget. Member
CRD-instance and Gatekeeper-constraint histories and committed missed-delete
repairs now share that token. Persisted UID/version receipts suppress unchanged
replay and same-current-version source overlap; atomic deletes preserve UID
replacements. The receipts do not order opaque versions across watches or form
a complete historical deduplication ledger. Retained metadata does not clear
known loss or establish continuous history. Standalone Tier 2 core, CRD-instance
and Gatekeeper collectors now share explicitly unlabelled graph receipts and
callback locks through the existing writer, including metadata-only watch
deletes and replay suppression. Standalone core, CRD-instance and Gatekeeper
collectors now also share explicitly scoped, bounded fresh-list missed-delete
repair with history enabled or disabled. It adds no watches, preserves named
member rows and UID/version replacements, and records metadata-only deletes
only after committed removal when history is enabled. CRD repair now also checks
retained versions still explicitly served by the same current CRD, using each
version's own complete list and shared round budgets. Served-set changes cancel
in-flight repair without resetting the selected watch. Failed/partial lists,
unversioned rows and unserved endpoints do not authorize deletion; repair does
not clear known gaps. Fully retired dynamic-source data now follows an explicit
retain-and-qualify policy: private stored writer receipts bind member, metadata
owner UID, endpoint, registration lifetime and object UID/version. Retirement
invalidates observation qualification without deleting graph data or inventing
history. Missing legacy receipts and post-restart lifecycle knowledge stay
unknown; a same-name replacement cannot adopt stored data without fresh delivery.
Analysis/API/CLI reports qualify unknown, retired or replaced stored sources.
Gatekeeper callbacks also recheck cancellation between persistence, extraction
and individual edge writes during template replacement, retirement and shutdown.
Local race regressions cover member/standalone scopes and both history modes;
already-started writes may still commit and are retained without compensating
cleanup. Cancellation preserves prior loss evidence and committed history.
CRD/Gatekeeper replay now preserves the stored writer with history enabled or
disabled: unchanged overlapping delivery still re-extracts edges without adopting
retired data. History-disabled versioned dynamic callbacks consult one stored
receipt under the existing resource lock; failed lookups cannot fall back to an
unconditional write. Both source orders, scope modes and history modes have local
race coverage; deployed storage cost and M4 qualification remain pending.
Core collection now also preserves the stored dynamic writer on unchanged
history-disabled delivery after a cold cache or informer recreation. Versioned
cache misses consult one stored receipt; unchanged cache hits retain their
existing resource-read/write fast path and still re-extract edges. Failed reads
cannot fall back to a write or poison the successful-version cache. Local tests
cover source qualification, scope/history modes, UID/version changes, cancellation,
point-read counts and later recovery; deployed storage cost remains pending.
Core/CRD/Gatekeeper upserts now qualify missing UID/version/API-version fields
independently of history. Active persisted incomplete observations record a
processing gap; failed/cancelled writes retain their separate failure semantics.
Incomplete rows cannot poison the core successful-version cache or suppress the
first subsequent qualified history observation. Local matrices cover scope/history
modes, cancellation, committed data, identity repair and sticky gaps; deployed
qualification remains pending.
Core/CRD/Gatekeeper watch deletes now use the same UID-bound atomic stored-receipt
guard with history on/off. Late tombstones preserve replacements and their
edges; unverifiable deletion stays retained without an unconditional fallback.
Core markers preserve newer UIDs; configured delete history and broadcasts follow
committed deletion only. Local regressions cover receipt races, scope, failures and cancellation;
short-mode discovery tests skip envtest control-plane startup.
Core upserts now stop subsequent extraction, Secret-reference and edge stages
after cancellation while retaining confirmed in-flight writes/history. Upsert
notifications follow confirmed graph writes, including edge-only replay recovery;
failed replays do not send false updates. Local fake-client shutdown and failure
regressions cover both scope/history modes and preserve active partial extraction.
The shared builtin extractor registry now also stops between individual
extractors when the parent context is cancelled or expired. It discards the
whole cancelled batch, including late dependency output, instead of starting
later target reads. Ordinary dependency failures under an active parent still
retain partial edges and run subsequent extractors. Local registry/concurrent
handoff and core/Gatekeeper callback regressions preserve committed resources,
writer receipts, history deduplication, sticky gaps and healthy replay recovery.
Shared Rego evaluation now lets waiting callers cancel independently. Failed
evaluators hand ownership to one active waiter under the existing mutex rather
than an unlocked fallback; cancelled late results are excluded from the cache.
Local phase-controlled race tests cover handoff, exact accounting, member-key
independence and real-engine cached replay. Cache capacity and evaluation budgets
remain unchanged; deployed shutdown/load qualification is still pending.
CRD Rego extraction now retains whole earlier successful modules when a later
module fails, alongside a sticky extraction gap. Evaluation still stops at the
first failure; malformed output from that module is rejected atomically and
failed results are not cached. The collector validates the complete returned
batch against its member/standalone scope before any edge write, and cancelled
evaluation starts no subsequent writes. Local real-engine and callback race
regressions cover failures, replay recovery, receipts and history modes;
real-cluster and M4 qualification remain pending.
Rego cache entries now also include the prepared entrypoint, so different queries
over identical source cannot reuse one another's results. Cache misses execute
the same compiled module used to form the key, including after a wait during
concurrent reload. Local phase-controlled race tests cover distinct entrypoints,
source/entrypoint reload, member scope, cached replay and failed compilation;
cache capacity and evaluation budgets remain unchanged.
Rego evaluation now bypasses result caching and shared in-flight ownership for
observations without the existing minimum version identity. Missing UID, resource
version or API version cannot reuse stale output or suppress fresh relationships.
Extraction retains the same timeout/recover and partial-result behavior; qualified
delivery restores normal caching without clearing the prior processing gap.
Local real-engine/CRD regressions cover scope/history modes, metadata-only history,
source receipts, cache accounting and recovery. Deployed cost and M4 remain pending.
Rego result caching now also separates full API versions. Equal UID/version
receipts cannot reuse rule output or wait on an evaluation from another served
representation; each representation keeps its own bounded replay entry. Local
engine and CRD endpoint-switch regressions cover raw/minimal input, matching and
wildcard rules, member/history modes and retained relationships. Existing fresh
representation writes and UID/version history suppression remain unchanged;
general version equivalence and deployed qualification are still pending.
Core/Gatekeeper extractor reads are now pinned to one member or explicitly
unlabelled standalone data through a narrow scoped lister. Core/CRD/Gatekeeper
validate whole edge batches before persistence, preventing foreign endpoints
and partial Secret-reference writes. Builtin Service selection and Kyverno report
lookup have local cross-member regression coverage; local report replay still
recovers results. Existing retained data is not cleaned or reowned, and these
scope guards do not close the complete-coverage or M4 gates.
Kyverno report-store failures now return usable edges with extraction-gap evidence;
successful empty report queries keep plain edges without a new failure gap.
Later target-read failures retain earlier matches; cancellation stops subsequent
lookups. Local regressions cover scope/history modes and normal replay recovery
without erasing the original gap or adding duplicate resource history.
Gatekeeper now also retains earlier target matches after a later kind/namespace
read failure and persists usable partial extraction with a sticky gap. Every
returned batch still passes the full scope guard before any edge write. Local
regressions cover violation attributes, cancellation, write failures, receipt
retention and replay across both scope/history modes; deployed acceptance remains
pending.
NetworkPolicy ingress/egress extraction now retains earlier peers and namespaces
after a later lookup fails, alongside the existing extraction gap. Cancellation
stops subsequent target reads, and failed-query partial rows are excluded. Local
regressions cover selection/peer results, member isolation, both history modes,
empty queries and replay recovery. These remain declarative relationships;
effective CNI reachability and M4 acceptance are not established.
General version equivalence, continuous retained history and verified recovery
points remain pending. Existing
v1 snapshot list/diff routes now authorize and read one attached member through
bounded scoped capabilities; unscoped alpha history aliases reject federation
access. Snapshot Web views now bind to the selected member, separate caches and
clear stale results/highlights during scope changes. Read-only `kubeatlas snapshot
list`/`diff` commands send explicit member scope without retries or API fallback.
The internal trigger now creates one member-specific marker from atomic scoped
stored-graph counts, behind the existing operator boundary and member visibility
checks. It never retries an unknown write outcome or clears history gaps.
The trigger CLI and single CronJob now pass explicit member selection and an
optional existing-Secret credential, with validated bounded receipts and no
redirect/application/Job retries. The chart retains its v1.6.0 image default;
new member flags require a matching development image. Deployed PostgreSQL
qualification and M4 acceptance remain pending.
The unreleased v1 impact API now authorizes cluster scope before reads and
combines the evidence under a five-second deadline and 2 MiB response cap.
Standalone CRD and Gatekeeper discovery now contribute separate bounded
list/watch, handler, and write-gap evidence, including registration continuity.
Gatekeeper also records its existing supported-template API check, with explicit
unknown, advertised, not-advertised, denied, failed, and stale qualification;
this is not installation or complete-inventory proof. Standalone background
discovery now enumerates bounded API metadata across advertised versions,
reports optional API presence, and identifies endpoints lacking captured
registration evidence. Discovery errors, stale data, and limits cannot prove
absence. Sequential API inventory now retains validated descriptors from earlier
completed requests when a later version request fails, marking the round partial
and stopping at that error. Cancelled rounds expose no prefix, and recovery uses
one new round without merging older evidence. Local discovery/recorder/analysis
and client-go HTTP regressions preserve scope and unknown API assessments for
partial evidence; request, descriptor and time budgets remain unchanged.
Partial aggregated discovery now also retains valid metadata already returned
for advertised versions outside the failure set, without per-version fallback
requests. Stale/missing versions remain excluded and the round stays partial;
failed and healthy versions share the same 64-version budget. Local client-go
HTTP regressions cover current/stale core discovery and mixed response formats,
with scope, descriptor caps and unknown API assessments preserved.
Startup resource selection now also validates canonical Kind/namespace scope and
rejects malformed or duplicate target descriptors. Unsupported optional APIs are
skipped; required ones fail selection, and discovery errors return no partial
startup set. Local independent-shape and client-go HTTP regressions cover all 25
canonical endpoints, cache reuse and custom endpoint compatibility. This remains
advertised metadata validation, not effective permission or watch-success proof.
Gatekeeper's separate ConstraintTemplate check now also rejects duplicate target
descriptors and inconsistent explicit row group/version before allowing template
list/watch. Local scope, recovery, fake-informer and client-go HTTP regressions
preserve failed qualification, prior write gaps and retained graph data. The
existing discovery deadline, polling cadence and no-client compatibility remain;
this does not qualify installation, watch reliability or M4 acceptance.
API inventory now excludes every descriptor for a duplicated endpoint within a
returned group/version, including malformed duplicates in either order and late
duplicates beyond candidate selection's descriptor cap. Independent valid rows
remain available in partial evidence. Both sequential and partial aggregated
discovery preserve scoped captures, unknown optional API assessments and recovery
from a fresh round. Candidate identity storage remains bounded by the existing
256-descriptor budget; validation uses the existing round deadline and adds no
request. Capped rounds remain limited even if exclusions reduce the retained set.
CRD registration now follows selected endpoint/kind/scope/UID changes,
with fresh coverage tokens, UID-bound deletion, and ordered in-flight callback
handoff. Unserved CRDs stop collection; their evidence and graph data remain
available without treating endpoint disappearance as deletion proof. Other still
served versions can qualify retained-row repair using their own fresh lists;
this does not establish version equivalence.
Federated member informers now run the same bounded API inventory worker
using each member's own kubeconfig and retain member-scoped cached evidence.
Member removal and shutdown join the worker before allowing same-name
replacement. Member bundles also run scoped CRD/Gatekeeper collectors, with
member-prefixed resource/edge identities, isolated source evidence, scoped rule
caches and extractor reads, and joined dynamic callback shutdown. Active member
endpoints now run bounded fresh-list reconciliation for missed deletes, with
atomic UID/version-checked removal and incident-edge cascading. Failed or partial
lists never authorize deletion, and successful repair does not reset historical
coverage gaps. Complete collector coverage, version equivalence and deployed
per-member history qualification remain
pending; registered types and inventory enumeration do not close those gaps.
The first Web slice now connects resource-detail and concrete topology nodes to
the server impact API, with explicit dependents/dependencies, bounded depth,
server counts and path highlights, separate authorization, and observation/history
warnings. Cluster switches and abandoned requests cannot reuse old results.
Expand **API observation details** to inspect captured before/after discovery
state, check time and stale/stopped/limited flags, optional API version/shape
assessments, and advertised endpoints without captured registration evidence.
Expanding the section makes no new request. Complete discovery enumeration does
not establish successful watches, effective permissions, or complete collector
coverage; empty endpoint lists cannot close those gaps.
The CLI now reads the same API envelope and exports text, JSON, or self-contained
HTML, with exact query/identity checks, bounded reads, private non-overwriting
file output, and no local fallback. Portable actual-handler tests establish
API/CLI/report capture parity. Web JSON/HTML downloads now retain the exact
validated response behind the visible result, without a new query or upload.
Each capture requires an explicit sensitive-topology acknowledgement; refresh,
selection changes, loading, and errors cannot export a stale result. Complete
wire-schema validation is locked to the Go API by a portable contract test.
A shared six-case synthetic matrix checks Web, CLI, and reports against the
Go API, including reference-only Secrets, separate authorization, and federated
scope isolation. Canvas filters do not narrow counts or add analysis requests.
Responsive analysis uses a narrow-screen overlay with focus/inert handling,
with bounded or wrapping shared controls. Portable client checks and deployed
integration remain distinct; M4 candidate acceptance is pending.
Synthetic client tests do not prove real-cluster watch reliability,
real-environment authorization configuration, effective permissions, or release delivery.
The M0 synthetic tests document
legacy behavior; they are not passing v1.7 feature acceptance. A final frozen
candidate is planned for one complete 72-hour observation, not 168 hours;
the protocol must be frozen before launch and interrupted runs never combine.

HA, built-in authentication, attack paths, GitOps drift/remediation, cloud
resource discovery, LLM diagnosis, and blanket dependency-major upgrades remain
outside this release. Older phase guides do not override this scope. Ecosystem
repositories retain independent release cadences; Backstage remains source-only.

## Related tools

KubeAtlas overlaps with a few other projects in the Kubernetes
observability / introspection space. The honest position is that the
overlaps are partial — these tools answer adjacent questions, and most
teams will end up running more than one. This section explains the
deliberate scope choices.

### vs. [Karpor](https://github.com/KusionStack/karpor)

[Karpor](https://github.com/KusionStack/karpor) is a Kubernetes
search and insight platform from the KusionStack ecosystem. It
overlaps with KubeAtlas only at the surface — both look at K8s
objects and surface relationships — but the two projects make
fundamentally different choices about who they're for and what
they optimise.

KubeAtlas differs in three ways that matter:

1. **KubeAtlas is an independent tool.** It isn't part of a larger
   platform, doesn't assume an ecosystem, and doesn't impose a
   workflow. You install it, point it at a cluster, get answers.
   Adoption costs nothing beyond the binary. Removal is the same
   `helm uninstall`. No long-term commitment to a vendor or stack.

2. **KubeAtlas focuses on dependency analysis** — specifically, the
   first-glimpse problem an infrastructure engineer faces when
   handed an unfamiliar cluster: *"what's running here, and how is
   it wired together?"* The answers are derived as a typed
   dependency graph (eight edge kinds today, more later) so
   questions like *"if I delete this Secret, what breaks?"* are
   one traversal, not a series of greps. Search is a side benefit;
   structure is the point.

3. **KubeAtlas is small, compact, and easy to onboard and extend.**
   The default deploy is a single Pod with no external dependencies
   — no search backend, no message queue, no sidecars. Opt into
   Tier 2 persistence (PostgreSQL + Apache AGE via a CNPG-managed
   `Cluster`) when you're ready; its cluster-scoped operator is an
   explicit prerequisite. Tier 1 onboarding is `helm install` plus
   a port-forward. Extending the edge schema
   is either one Go file plus one test (see
   [Adding a new edge type](./developer-guide.md#adding-a-new-edge-type--a-worked-example))
   or a [Rego rule pack](./concepts/rego-rules.md) loaded at
   runtime from an OCI artifact — no rebuild required. The codebase
   is small enough that a contributor can read the full graph
   engine in an afternoon.

Karpor and KubeAtlas can coexist on the same cluster — they answer
different questions. If you need cross-cluster search, indexing,
and a centralized insight pipeline, Karpor is the right shape for
that. If you need to understand a single cluster's structure
right now with a tool you can install in five minutes and remove
just as easily, KubeAtlas is the right shape for that.

### Other tools you might evaluate alongside

A short list, with the question each is best at:

- **[Headlamp](https://headlamp.dev/) / [Lens](https://k8slens.dev/)** — "Show me everything in this cluster, navigably." General-purpose K8s UIs. KubeAtlas ships its own UI for the graph; a Headlamp plugin (shipped in v1.1) lives in `lithastra/kubeatlas-headlamp-plugin`.
- **[`kubectl tree`](https://github.com/ahmetb/kubectl-tree)** — "Show owner-reference children of this object." Kubectl plugin, single-edge-type, terminal-only. KubeAtlas covers the same ground via the OWNS edge plus seven others, with a server and UI on top.
- **[Argo CD](https://argoproj.github.io/cd/)** — Resource topology, but framed around an Application as the root. KubeAtlas roots are arbitrary; you don't need GitOps adoption.
- **Prometheus / Grafana / DataDog** — Metric and event observability. Disjoint problem space.

If you have a tool that should be on this list, [open a doc PR](https://github.com/lithastra/kubeatlas/blob/main/CONTRIBUTING.md).

## v0.1.0 (released)

The first publicly-releasable build. **Shipped on 2026-05-06.**
Install reference: see [Quick Start](./quick-start.md). Release
notes:
[github.com/lithastra/kubeatlas/releases/tag/v0.1.0](https://github.com/lithastra/kubeatlas/releases/tag/v0.1.0).

Delivered scope:

- **REST API** — `GET /api/v1alpha1/graph` at four levels
  (cluster / namespace / workload / resource), `GET /resources/{ns}/{kind}/{name}` for detail, `GET /search`, `/healthz`, `/readyz`, `/metrics`
- **WebSocket** — `/api/v1alpha1/watch` for live graph updates
- **Web UI** — React 19 + TypeScript + MUI v5, technology-stack-aligned with [Headlamp](https://headlamp.dev/) so a future Headlamp plugin is a port rather than a rewrite
  - Cytoscape topology view (cluster / namespace / workload levels)
  - Mermaid neighbor view (single resource + one hop)
  - DataGrid resource list with namespace filter
- **Helm Chart** — `oci://ghcr.io/lithastra/charts/kubeatlas` with secure defaults baked in:
  - `service.type: ClusterIP` (no automatic LoadBalancer exposure)
  - Ingress disabled by default; enabling it requires explicit `acknowledgeNoBuiltinAuth=true` (schema-validated)
  - RBAC ClusterRole hard-coded to `[get, list, watch]` (KubeAtlas is read-only, always)
  - Pod runs as non-root with read-only root filesystem
- **Distribution** — multi-arch container image (linux/amd64, linux/arm64) on `ghcr.io/lithastra/kubeatlas`, four-platform binaries via goreleaser, Helm Chart published as OCI artifact
- **Docs site** — quick-start, installation guides per Ingress flavour, architecture, FAQ, this roadmap

### Explicitly *not* in v0.1.0

So v0.1.0 shipped something usable instead of trying to ship
everything:

- ❌ Built-in authentication — operators provide it via the Ingress layer (oauth2-proxy / Pomerium / Cloudflare Access; example values shipped)
- ❌ Persistence — Tier 1 in-memory only; restart loses graph state
- ❌ Multi-cluster — one kubeconfig per KubeAtlas instance
- ❌ Custom edge types — the eight built-in edges were it for v0.1.0
- ❌ Dynamic CRD discovery — the 16 watched GVRs were hard-coded
- ❌ RBAC graph and NetworkPolicy graph
- ❌ Historical snapshots / diff
- ❌ Dark mode
- ❌ Headlamp plugin

The first seven shipped in v1.0; the Headlamp plugin shipped in
v1.1.

## v1.0 (released)

The "make it suitable for production observability" cycle.
Shipped scope:

| Theme | What landed |
|---|---|
| **Persistence** | Tier 2 storage on PostgreSQL ≥ 14 with the [Apache AGE](https://age.apache.org/) extension. Opt-in via `persistence.enabled=true`; embedded mode (`persistence.embedded.enabled=true`) creates a `Cluster` managed by the separately installed [CloudNativePG](https://cloudnative-pg.io/) operator. Restart now preserves the graph; informer cold-start drops to ~4 s reading the persisted state. |
| **Extensibility** | [Rego rule packs](./concepts/rego-rules.md) — declare CRD edges in Rego, no rebuild. Packs are OCI-distributed and signed. Embedded OpenShift pack auto-loads when `route.openshift.io` is detected; extras load via `rulePacks.extras`. Dynamic CRD discovery is built in — KubeAtlas walks the cluster's CRDs and registers per-CRD informers at runtime. |
| **More edge kinds** | [RBAC graph](./api-reference.md) — `BINDS_SUBJECT` and `BINDS_ROLE` edges plus three new endpoints (`/api/v1/rbac/serviceaccount/<ns>/<name>/permissions`, `/api/v1/rbac/role/<ns>/<name>/subjects`, `/api/v1/rbac/clusterrole/<name>/subjects`). |
| **Impact radius** | [Blast radius](./concepts/blast-radius.md) — `/api/v1/blast-radius/<ns>/<kind>/<name>` walks incoming edges and returns the affected set. Folded into the v1 resource-detail bundle as `blastRadiusCount`. |
| **Orphans + cycles** | [Detection](./concepts/orphan-cycle.md) — two new endpoints for stale resources and Tarjan-detected SCCs. |
| **API surface** | `/api/v1/*` GA endpoints, frozen `/api/v1alpha1/*` retained — see [API versioning](./concepts/api-versioning.md). |
| **TLS** | [cert-manager Helm integration](./installation/cert-manager.md) — selfsigned / letsencrypt-staging / letsencrypt-prod / custom. |
| **OpenShift** | [Auto-detect + install path](./installation/openshift.md) plus the weekly e2e-openshift-local (CRC) workflow. |
| **Performance** | 5K-resource perf bench + regression gate; recursive-CTE traversal so blast-radius p95 stays under 500 ms on a 7K-resource cluster. |
| **Chaos** | `test/chaos/` scenarios (pg-disconnect, rego-panic, rego-runaway, cert-manager-flap) gating the release. |

### Shipped in v1.1

These were in the original v1.0 plan but moved out for the
v1.0 cut and landed in v1.1:

- Headlamp plugin (`lithastra/kubeatlas-headlamp-plugin`)
- Historical snapshots / diff

## v1.1 / v1.2 / v1.3 (released)

These releases widen KubeAtlas beyond a single cluster's core resources
and reaches it from places besides the in-cluster UI. It ships in
three releases.

### v1.1 (released) — rule packs, snapshots, search, plugins

Shipped scope:

| Theme | What landed |
|---|---|
| **Cloud-platform rule packs** | Opt-in EKS / AKS / GKE add-on CRD packs in the sibling `lithastra/kubeatlas-rules` repo — AWS Load Balancer Controller, Karpenter, GKE Ingress, Multi-cluster Services, and more. Loaded via `rulePacks.extras`. |
| **Historical snapshots** | An append-only resource-change event stream with `GET /api/v1/snapshots/diff` — "what changed in the last hour?" Tier 2; configured under `snapshots.*`. |
| **Full-text search** | Ranked `GET /api/v1/search` over resource name, kind, namespace, and label values; indexed on Tier 2. |
| **Label filtering** | `label.<key>=<value>` narrowing on the cluster / namespace views, plus a `GET /api/v1/labels` vocabulary endpoint. |
| **NetworkPolicy edges** | `NetworkPolicy` is first-class — the Pods a policy selects and the peers it allows. |
| **Rule-pack signing** | Keyless Sigstore signature verification for OCI rule packs (`rulePacks.verifySignature`). |
| **Plugins** | The `kubectl atlas` plugin and a [Headlamp](https://headlamp.dev) plugin (separate repo). |

### v1.2 (released) — offline rendering and a self-contained plugin

KubeAtlas usable without a server in the cluster. Shipped scope:

- **Offline `kubectl atlas`** — the plugin builds the dependency
  graph straight from the Kubernetes API and renders it locally:
  a static SVG by default, or an interactive in-process web UI
  with `--local-ui`. The plugin is self-contained — it no longer
  needs a separate `kubeatlas` binary.
- **Graph-image export** — `kubeatlas -once -format=svg` and a
  `GET /api/v1/export` endpoint render cluster / namespace views
  as SVG or PNG.
- **Cluster selection** — the CLI and the plugin honour the
  standard `--context` / `--kubeconfig` flags.
- **Rule-pack signature verification on by default** —
  `rulePacks.verifySignature` defaults to `true`; air-gapped
  installs must set it `false` explicitly.

### v1.3 (released) — multi-cluster, platform identity, cartography UI

The final release in this line. Stretching the graph across cluster
boundaries and replacing the Headlamp-styled web shell with a
purpose-built cartography UI:

- **Multi-cluster federation** — one KubeAtlas instance, N
  clusters. A new `pkg/multicluster/` package, a `ClusterID` on the
  graph model, federation aggregator and `/federation` route group,
  and cluster-scoped WebSocket subscriptions.
  `KUBEATLAS_MULTICLUSTER_KUBECONFIG_DIR` points at a directory of
  per-cluster kubeconfigs (one file per cluster, filename = cluster
  ID). `GET /api/v1/federation/{clusters,graph}` is the read surface;
  the Web UI **LeftClusterStrip** is wired to it for cluster picking.
- **Platform-identity edges** — `BINDS_PLATFORM_IDENTITY`
  from a ServiceAccount to a synthetic `ExternalIdentity` representing
  the cloud account it is bound to:
  - **EKS** — `eks.amazonaws.com/role-arn` annotation.
  - **AKS** — `azure.workload.identity/client-id` label.
  - **GKE** — `iam.gke.io/gcp-service-account` annotation.
- **HorizontalPodAutoscaler support** — new `SCALES` edge type from
  an HPA to whatever its `spec.scaleTargetRef` names (Deployment /
  StatefulSet / any /scale-bearing kind).
- **`kubeatlas-action`** — a new repo `lithastra/kubeatlas-action`
  so KubeAtlas can run in GitHub Actions CI pipelines, rendering the
  dependency graph as an SVG artifact.
- **Cartography Web UI redesign** — the whole web shell rebuilt
  around the "one graph, many modes" design:
  - 5 runtime-switchable themes (Parchment / Survey / Terrain /
    Ink / Slate) sharing one CSS-variable contract.
  - Persistent time axis with diff-mode anchor presets (1h / 4h /
    24h / 7d) that highlight added / removed / modified resources
    on the canvas.
  - Blast-radius mode (BFS from selected node with depth +
    direction controls, canvas dim/brighten, hop-by-hop summary).
  - ⌘K command palette over `/api/v1alpha1/search` with matched
    nodes highlighted on the canvas.
  - Zoom-scale widget mapping cytoscape zoom × → L1–L4 bands.
  - Edge-type filter presets (All / RBAC / Network / Config /
    Storage) that fold a sub-graph into the canvas without route
    changes.
  - Left cluster strip wired to the federation cluster list with
    deterministic per-cluster chip colours.
- **v1.3 perf baseline** — dual-tier (Tier 1 + Tier 2) on the
  10K-resource stress fixture; multi-cluster merge bench and a
  cluster-disconnect chaos scenario.

### v1.3.1 (shipped)

Polish items deferred from v1.3.0, now shipped in v1.3.1:

- **Cluster picker → federation graph wiring** — the
  LeftClusterStrip routes the selected cluster through
  `/api/v1/federation/graph?cluster=…` with per-cluster border
  tints via a deterministic hash palette.
- **Drag-anchor on the time-axis rail** — the rail is a real ARIA
  slider with drag + keyboard support and a 30s right-edge snap.
- **Keyboard graph traversal** — Arrow keys walk the node list,
  Enter/Space opens the detail panel, Esc clears.
- **Screen-reader announcements** — polite `aria-live` region on
  blast-radius enter, diff anchor change, cluster focus, and
  command-palette open.
- **Slate theme WCAG AA contrast fix** — `text-3` bumped from
  `#6B7079` to `#888E98`.
- **RadialMenu + right-click depth picker** — enter blast-radius
  mode at depth 1 / 3 / ∞ in a single gesture.
- **`kubectl atlas --version`** — stamps version + commit + date.
- **goreleaser `dockers_v2`** — single buildx-native block.
- **RELEASING.md generalised** — steady-state recipe for all
  future releases.

### v1.3.x remaining follow-ups

- **FLIP zoom transitions** — the zoom-scale widget animates the
  cytoscape zoom level today; the aggregated → expanded node
  split/merge with the design's 400ms FLIP choreography is queued.

## v1.4 / v1.5 (released)

### v1.4 (shipped) — offline diagnostics, policy visibility, telemetry

- **Offline diagnostic report** — `kubeatlas diagnose` and
  `GET /api/v1/diagnose` produce a self-contained HTML/JSON snapshot
  (graph, orphans, cycles, top blast radius) from an offline scan,
  for air-gapped audits and CI. The JSON carries a normalized
  `policyViolations` array.
- **Policy visibility** — Gatekeeper Constraints and Kyverno
  policies surface as `ENFORCES` edges (with violation status) via a
  dynamic informer-of-informers that discovers Constraint CRDs at
  runtime. New `/api/v1/policy/constraints` + `/affected` endpoints
  and a Web UI Policy view.
- **Opt-in anonymous telemetry** — off by default; when enabled,
  sends coarse non-identifying usage once a day to a hard-coded
  endpoint, with a transparent `/api/v1/telemetry/preview` and a
  documented [trust contract](./concepts/telemetry-schema.md).
- **v1alpha1 usage counters** — `kubeatlas_api_v1alpha1_requests_total`
  vs `kubeatlas_api_v1_requests_total`, the data that will inform a
  future decision on retiring `v1alpha1`.
- **Ecosystem** — Backstage plugin (v0.1), Headlamp Policy view, and
  a GitHub Action `policy-report` option.

### v1.5 (released) — OTel overlay, multi-cluster RBAC, GraphStore v2

v1.5 is a deliberately **non-breaking minor release**. The public HTTP
API is only added to, never changed: `v1alpha1` stays byte-for-byte
frozen, and there is **no v2.0** on the committed roadmap.

- **OpenTelemetry runtime overlay** (F-204) — observed
  `CALLS_AT_RUNTIME` edges, correlated from OTLP traces and layered
  over the declarative graph via `GET /api/v1/otel/overlay` (opt-in,
  Tier 2 only). See [OpenTelemetry overlay](./concepts/otel-overlay.md).
- **Read-side multi-cluster RBAC visibility** (F-206) — filter which
  clusters a caller sees on the federation surface, keyed on its bearer
  token. Visibility only — no credential fetch/rotation, no OIDC. See
  [Multi-cluster & RBAC visibility](./installation/multicluster.md).
- **Internal GraphStore v2** — a purely internal interface clean-up
  (verb standardisation + a `StoreVersion` exposed on `/api/v1/info`),
  invisible to the HTTP API. It ships **inside v1.5** and does not
  imply a v2.0.
- **Ecosystem parity** — the Headlamp plugin (v1.2.0) adds an OTel
  Overlay view (+ TraceTimeline linking to Jaeger/Tempo); the
  Backstage plugin reaches v1.0.0 GA at Headlamp parity, adding an
  Admission-policies card (F-205) and a Runtime-calls card (F-204).

## v1.6.0 — production operability {/* #v16-planned--production-operability */}

v1.6 is a reliability release for DevOps teams operating KubeAtlas on
vanilla Kubernetes. It does not widen the product surface. Its release
contract is that an operator can install verifiable public artifacts on a
supported Kubernetes cluster, upgrade directly from v1.5.2, restore Tier 2
after deliberate data loss, and measure stability under a declared workload without
weakening KubeAtlas's read-only or Secret-data boundaries.

The following is the **original planning contract**, preserved for context.
The released outcome and approved observation-scope reduction are recorded
above and in the [release matrix](./release-process.md). In particular,
performance and endurance targets below are not passing v1.6.0 evidence.

Tracking lives in the
[v1.6 Production Operability milestone](https://github.com/lithastra/kubeatlas/milestone/2).
The implementation order is the supported
[platform baseline](https://github.com/lithastra/kubeatlas/issues/23),
[upgrade and recovery](https://github.com/lithastra/kubeatlas/issues/24),
[scheduled evidence and runbooks](https://github.com/lithastra/kubeatlas/issues/25),
[performance and endurance](https://github.com/lithastra/kubeatlas/issues/26),
then the final
[core artifact audit](https://github.com/lithastra/kubeatlas/issues/27).

### Original planning scope (historical) {/* #committed-scope */}

- **Supported platform baseline** — at the v1.6 code freeze, support and test
  only the three Kubernetes minor branches still maintained upstream. The
  Helm `kubeVersion`, documentation, kind images, and release matrix must name
  the same frozen range. The planning matrix begins with Kubernetes
  1.34–1.36; it must be refreshed if upstream support changes before the
  release cut.
- **Supported persistence dependencies** — move the external CloudNativePG
  prerequisite to a then-supported, security-fixed release and document the
  operator upgrade chain from the v1.5.2 prerequisite. Keep PostgreSQL on
  major 16 to avoid an unrelated major-version migration, but rebuild on the
  current PostgreSQL 16 patch release at code freeze. Keep the pinned Apache
  AGE PG16 1.6.0 release commit unless an upgrade or restore test proves that
  a change is required.
- **One upgrade path** — automate a clean v1.5.2 → v1.6.0 Tier 2 upgrade,
  including the separately owned CloudNativePG prerequisite, database-image
  update, application migration, initial re-sync, and failure diagnostics.
  KubeAtlas must not bundle, take ownership of, or silently upgrade the
  cluster-scoped operator.
- **One Tier 2 recovery path** — provide one provider-neutral, end-to-end
  backup and restore procedure for the embedded CloudNativePG path. Restore
  into a fresh database or namespace after deliberately deleting the original
  database. BYO PostgreSQL remains operator-owned: KubeAtlas documents its
  data and extension contract but does not claim to automate every provider.
- **Continuous compatibility evidence** — run a scheduled clean-cluster test
  from public artifacts at least weekly, and run the frozen Kubernetes matrix
  before release. PR-only source builds do not prove that registries, charts,
  external operators, and public installation instructions still compose.
- **Performance and endurance gates** — verify the default resource profile
  at approximately 5K resources and a documented production profile at
  approximately 10K resources. Preserve the existing representative-workload
  targets of cluster and namespace view p95 ≤ 1 s and blast-radius p95 ≤
  500 ms. Evaluate endurance after the final dependency and instrumentation
  changes, with workload, duration, and limits declared before the run.
- **Operator visibility and runbooks** — make it possible to determine whether
  the graph is synced or stale, the database is reachable and durable,
  events or snapshots were dropped, and the most recent backup is too old.
  Ship product-neutral metrics, alert examples, and current chaos/recovery
  runbooks; do not require a Prometheus Operator CRD or a specific dashboard.
- **Core cluster artifact trust** — keyless-sign the application image,
  PostgreSQL + AGE image, and Helm OCI chart by digest. Produce and verify
  SBOM and provenance attestations for both runtime images. Verification from
  an anonymous clean environment, constrained to the expected GitHub Actions
  OIDC identity, is a release gate.
- **Security invariants everywhere** — retain read-only Kubernetes RBAC and
  reference-only Secret nodes. Secret objects, values, identifying metadata,
  database credentials, and federation kubeconfigs must remain absent from
  APIs, the Web UI, database rows, history, backups, logs, diagnostics,
  exports, rule inputs, and telemetry. A random sentinel scan gates every
  upgrade, restore, and soak run.

### Original acceptance targets (historical, not a pass report) {/* #v160-release-gates */}

| Gate | Required evidence |
|---|---|
| Fresh install | Tier 1 and embedded Tier 2 install successfully across the frozen Kubernetes matrix. |
| Upgrade | A real public v1.5.2 Tier 2 installation upgrades to the candidate without data-contract or readiness failure. |
| Recovery | A protected backup restores into a fresh database or namespace after deliberate deletion; retained history and rebuildable graph data match the documented contract. |
| Dependency failure | API-server and PostgreSQL interruptions are visible and KubeAtlas returns to a healthy, synced state within 120 seconds after the dependency becomes available. |
| Endurance | Report the exact candidate, continuous observation duration, and failure/recovery drills, with no unexplained crash, OOM, restart, silent event loss, or unbounded memory, goroutine, or queue growth. A shorter functional check is not endurance evidence. |
| Performance | The 5K default and 10K documented profiles meet their frozen latency and resource thresholds; pathological layouts are recorded separately rather than hidden. |
| Secret boundary | The sentinel is absent from every collected runtime, persistence, backup, export, diagnostic, log, and telemetry artifact. |
| Artifact trust | Anonymous pulls and identity-constrained signature, SBOM, and provenance verification pass for every core cluster artifact. |
| Documentation | The support matrix, single-replica outage, external-auth requirement, backup sensitivity, upgrade ordering, and rollback limits match observed evidence. |

Freeze observation duration, warm-up, baseline windows, and RSS, goroutine,
and queue-depth thresholds before a run. Keep workstation orchestration and
run-specific evidence outside the repository; removing local tooling does
not make an incomplete or failed validation pass.
Injected restarts are recorded separately; all other restarts fail the gate.
Normal load permits no dropped events or snapshots. An intentional overload
may shed work only when the loss is counted, logged, and alertable.

Record any release-owner-approved scope exception and its coverage limits
separately. Do not relabel failures, join interrupted attempts, or claim that
functional checks establish long-term reliability.

### Explicit non-goals

- Multiple KubeAtlas application replicas, leader election, active/active HA,
  or zero-downtime application upgrades. Tier 2 remains a single-application-
  replica deployment using `Recreate`.
- Built-in authentication, OIDC/SSO, sessions, per-user response filtering,
  access audit, or deeper multi-tenancy. Production exposure stays behind an
  operator-managed authentication layer.
- EKS, AKS, GKE, managed-database, object-store, or backup-provider-specific
  integrations. The acceptance environment is vanilla Kubernetes, without
  requiring a particular developer workstation or container runtime.
- New graph edge types, cloud-resource discovery, cross-cluster inference,
  automatic remediation, Kubernetes write permissions, or a Web UI redesign.
- `/api/v2`, removal of `/api/v1alpha1`, or any other breaking public API
  change. The v1.x additive compatibility gate remains in force.
- Headlamp, Backstage, Krew, GitHub Action, rule-pack, or upstream-catalog
  publication as a core v1.6.0 blocker. Those repositories retain independent
  version and review cadences.
- Parallel implementations for multiple backup providers, direct upgrades
  from arbitrary historical releases, or a maintained v1.5 support branch.
- Relaxing the v1.5.2 Secret boundary, or adding a general redaction system for
  non-Secret cluster data. ConfigMap values, workload specs, RBAC, names, and
  other topology remain observable and potentially persistent, so exposing
  KubeAtlas still requires an explicit information-leak warning.
- Signing every downloadable CLI archive. v1.6 first closes the trust chain for
  the OCI chart and images used by the production cluster path; binary archives
  retain checksums and must not be described as signed.

Generated API clients are a stretch goal, not a v1.6.0 gate. The main-repo Web
client may adopt generated types if that change stays additive and does not
delay the committed operability work. Headlamp and Backstage consume the
published API schema on their independent release cadence.

### Support and rollback policy

- v1.6 supports a direct upgrade from the latest v1.5.2 release only. Older
  installations move to v1.5.2 first.
- Only the latest v1.6 patch is maintained; the
  project does not run parallel v1.5 and v1.6 maintenance branches.
- Database downgrade is not promised. Recovery from a failed irreversible
  migration uses the protected pre-upgrade backup and documented restore path,
  not an older binary against the migrated database.
- Core current-state graph data is rebuildable from Kubernetes; retained
  history and other persistence-only data must be called out separately in the
  backup contract.

### Deferred — future human decisions, not commitments

- Retiring / removing the `v1alpha1` API — driven by the usage
  counters, and only ever as a tracked, announced deprecation that
  versions a v2.0. Not scheduled.
- Multi-cluster credential auto-rotation, OIDC/SSO, and deeper
  tenancy beyond read-side visibility.

## Beyond v2.0 (sketch)

Direction, not commitment:

- **Third-party Kubernetes platform deep-dives** — going beyond
  the identity edges v1.3 lands:
  - **Amazon EKS** — recognise EKS-Anywhere quirks; deeper Karpenter / AWS LBC modeling.
  - **Azure AKS** — surface AKS-specific add-ons beyond Workload Identity.
  - **Google GKE** — recognise Autopilot resource constraints.
  - **Red Hat OpenShift** — model `Route` as a first-class edge alongside `Ingress`/`HTTPRoute`, recognise `DeploymentConfig` and `BuildConfig` natively, ship installation docs that work with OpenShift's stricter SCC defaults.
  Verified install paths, platform-specific Helm values examples, and an integration-test matrix per platform.
- **Cloud-resource integration** — surface AWS/GCP/Azure objects (S3 buckets, IAM roles, Cloud SQL instances) that K8s resources reference, so the graph spans the cluster boundary. Builds on the platform-identity edges from v1.3 (the IAM/identity bindings give us the edges to follow off-cluster).

## Compatibility promises

- **From v0.1.0 onward**: semver. A field added to `graph.Resource` or `graph.Edge` is a minor-version event; renaming or removing one is a major-version event.
- **`-once` CLI mode** stays available across the v1.x line as a scriptable scrape path.
- **Helm values schema** changes additively where possible; breaking renames are called out in CHANGELOG and accompany a migration note.

## How to influence the roadmap

- **Open an issue** on [GitHub](https://github.com/lithastra/kubeatlas/issues) describing the use case (not the proposed solution).
- **Reactions on existing issues** are read as priority signal.
- **PRs welcome** for items already on the roadmap; for items not on it, open an issue first so we can talk shape before you spend time.
- v1.3 scope is set (multi-cluster + platform identity + Action), but the order **within** v1.3 — and what lands first in upcoming releases — will partly reflect what current users ask for first. If your team would adopt a future release conditional on a particular feature, say so in an issue.

---
sidebar_position: 4
title: Orphans & cycles
---

# Orphans & cycles

Two graph-shape problems show up with depressing regularity in
real clusters: **orphans** (resources nothing depends on, that
shouldn't be roots) and **cycles** (dependency loops that
shouldn't exist at all). KubeAtlas surfaces both as first-class
endpoints so they're discoverable from a dashboard or a CI gate.

## Orphans

An orphan is a resource that:

- has zero incoming edges, AND
- is not a *top-level* kind (a kind that conventionally has zero
  incoming edges by design — Namespace, Node, Deployment, etc.).

Plus a special case for Pods: a Pod with no `ownerReferences` is
flagged as `standalone_pod`, distinct from "orphan", because
many users `kubectl run` ad-hoc Pods on purpose. The reason
field lets dashboards render different copy.

### API

```bash
GET /api/v1/orphans
GET /api/v1/orphans?namespace=demo
```

```json
{
  "reports": [
    {
      "resource": { "kind": "ReplicaSet", "namespace": "demo", "name": "ghost-rs" },
      "reason":   "orphan"
    },
    {
      "resource": { "kind": "Pod", "namespace": "demo", "name": "lonely" },
      "reason":   "standalone_pod"
    }
  ],
  "count": 2
}
```

### Top-level whitelist

These kinds never appear in the orphans list, no matter what
their incoming-edge count is:

- Cluster-scoped roots: `Namespace`, `Node`, `PersistentVolume`,
  `StorageClass`, `ClusterRole`, `ClusterRoleBinding`,
  `CustomResourceDefinition`.
- Namespaced kinds users / GitOps systems author directly:
  `Deployment`, `StatefulSet`, `DaemonSet`, `Service`, `Ingress`,
  `Gateway`, `HTTPRoute`, `ConfigMap`, `Secret`, `ServiceAccount`,
  `Role`, `RoleBinding`, `Job`, `CronJob`,
  `PersistentVolumeClaim`, `NetworkPolicy`.

Anything else with zero incoming edges is suspect — typical
catches:

- A `ReplicaSet` whose `Deployment` was deleted with
  `--cascade=orphan`.
- A `Job` template (a CronJob's child Job that lost its CronJob).
- A custom resource whose owner CRD was uninstalled.

### What orphans does *not* tell you

- It doesn't say *why* the upstream went away. The graph encodes
  the current state, not the history; pair with `kubectl get
  events` or your audit log if you need the cause.
- It doesn't auto-clean. KubeAtlas is read-only by design.
  Removing the resource is a `kubectl delete` you make
  consciously after seeing the report.

## Cycles

A cycle is a strongly connected component (SCC) of two or more
resources. Trivial single-vertex SCCs (resources that point at
themselves) are not reported — they're either extractor mis-fires
or legitimate self-references and would only spam dashboards.

In a healthy cluster the cycles list is **empty**. Anything
non-empty is an investigate-immediately signal: K8s won't allow
OwnerReference cycles by construction, so a non-empty cycle list
means an extractor is over-reaching, a custom resource has a
genuine config error, or someone has been hand-editing
references.

### API

```bash
GET /api/v1/cycles
```

```json
{
  "cycles": [
    {
      "members": [
        { "kind": "ConfigMap", "namespace": "demo", "name": "a" },
        { "kind": "ConfigMap", "namespace": "demo", "name": "b" }
      ],
      "category": "unknown"
    }
  ],
  "count": 1
}
```

Members within a cycle are sorted by ID for diff stability;
multiple disjoint cycles each get their own object.

### Cycle categories

Real K8s clusters carry a handful of cycles that are
operationally benign — most commonly a webhook controller that
owns its own TLS Secret AND consumes the same Secret to terminate
TLS on its webhook. Tarjan sees these as cycles (because they
are), but treating them as actionable would spam every
dashboard. v1.0.1 onwards each cycle is tagged with a
`category` field so verifiers and CI gates can branch:

| Category | Meaning | Action |
|---|---|---|
| `bootstrap-cert` | 2-member cycle where one member is a `Secret` owned (via `ownerReferences`) by the other. Pattern shipped by cert-manager / CNPG / kyverno / kubeatlas itself. | Treat as benign. Exclude from "real cycle" counters. |
| `intentional` | Any member carries `metadata.annotations["kubeatlas.io/intentional-cycle"] = "true"`. The operator has declared the cycle is deliberate. One annotated member is enough — useful in multi-team setups. | Treat as benign. Audit the annotation if it shows up unexpectedly. |
| `unknown` | Every other cycle. | **Actionable**: investigate. Usually means a recent extractor or operator change introduced a real dependency loop. |

Precedence: `bootstrap-cert` > `intentional` > `unknown`. A
Secret in a 2-cycle that *also* carries the
`intentional-cycle` annotation is reported as `bootstrap-cert`
because the structural match is more specific.

Clients should treat any future / unrecognised category value as
`unknown` — the enum is append-only and adding categories
(e.g. `mtls-mesh`, `gitops-flux`) is not a breaking change.

### Algorithm

Tarjan's SCC algorithm — `O(V + E)`. The playbook prescribes
this specifically over a hand-rolled DFS + visited set: the
textbook implementation is correctness-tested and the perf
budget on 5K-vertex / 5K-edge graphs is ~80ms with the race
detector enabled, well under the 200ms target.

Dangling edges (target node not in the snapshot) are dropped
silently before Tarjan runs so the algorithm sees a closed
vertex set.

## Folded into resource detail (`/api/v1/...`)

The v1 surface carries `isOrphan` and `inCycle` booleans on the
resource-detail bundle so the UI can render badges per row
without a follow-up round-trip. See
[Blast radius](blast-radius#folded-into-resource-detail-apiv1).

## CI gate

Two sample uses worth knowing about:

- A scheduled job that hits `/api/v1/cycles` and pages oncall
  when `count > 0`. False positives should never happen — if
  one fires, the cluster has a real problem.
- A pre-prod CI step that hits `/api/v1/orphans?namespace=...`
  for the namespace under test, and fails the build when
  the report is non-empty. Catches "PR removed the Deployment
  but forgot the Service" classes of mistakes early.

The integration test in `test/verify/phase2.sh` (Part 3 / 4)
exercises both endpoints on a fixture cluster — the orphan path
applies a `ghost-rs` ReplicaSet and confirms it appears; the
cycle path confirms the endpoint stays empty on a healthy
fixture.

## What if the orphans list is wrong on my cluster

The most common cause: you have a CRD whose owner field is
*not* populated as a standard `ownerReferences` link. The OWNS
extractor only looks at `metadata.ownerReferences`; if your CRD
encodes its parent in `spec.ownerName` or similar, write a Rego
rule that emits the OWNS edge — see
[Rego rules](rego-rules).

Once the rule is loaded, the orphans report will start treating
those resources as having an upstream and they'll fall out of
the list automatically.

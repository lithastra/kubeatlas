# 0013 — CloudNativePG operator lifecycle

- Status: accepted
- Date: 2026-07-27
- Task: v1.5.1 stabilization / P0-3

## Context

KubeAtlas v1.5.0 declared the CloudNativePG operator chart as an
optional child dependency of the KubeAtlas chart. CloudNativePG
0.22.1 renders its CRDs from ordinary templates rather than Helm's
special `crds/` phase. On a clean cluster, Helm therefore validates
the KubeAtlas `postgresql.cnpg.io/v1` `Cluster` before API discovery
knows that kind and aborts the whole install.

The child-chart design also couples a cluster-scoped operator to one
application release. That makes ownership ambiguous when several
KubeAtlas releases share a cluster, and makes application uninstall
capable of removing control-plane resources that other PostgreSQL
clusters may need.

## Decision

CloudNativePG 0.22.1 is an explicit, cluster-scoped prerequisite for
embedded Tier 2. The operator is installed once in `cnpg-system`;
the KubeAtlas chart owns only its namespaced `Cluster` resource and
the application resources that consume the operator-managed Secret
and Service.

The KubeAtlas `Cluster` carries
`helm.sh/resource-policy: keep` by default. `helm uninstall
kubeatlas` removes the application but retains the database and its
PVC. Permanent data deletion is a separate, explicit operation:

```bash
kubectl delete cluster.postgresql.cnpg.io kubeatlas-pg -n kubeatlas
```

Operators who intentionally want the old delete-on-uninstall
behaviour can set `persistence.embedded.retainOnDelete=false`.

For a v1.5.0 upgrade, the external operator is installed with Helm's
`--take-ownership` option before upgrading KubeAtlas. This transfers
the existing CNPG CRDs to the cluster-scoped operator release while
the v1.5.0 bundled operator is still available. Upgrading to v1.5.1
then removes only the obsolete bundled operator.

## Rejected alternatives

### Keep CloudNativePG as a KubeAtlas child chart

Rejected because it does not solve clean-cluster API discovery and
creates multiple competing operators when more than one KubeAtlas
release exists.

### Copy CloudNativePG CRDs into the KubeAtlas chart's `crds/` folder

Rejected because KubeAtlas would become the owner and upgrade path
for a large external CRD surface. Helm also does not upgrade or
delete objects installed through `crds/`, so the apparent
single-command install would hide a separate lifecycle anyway.

### Install CRDs or the operator from Helm hooks

Rejected because Helm validates rendered custom resources before a
hook can make their kinds discoverable. Hook jobs would also need
privileged cluster credentials and introduce partial-install
recovery states.

## Consequences

- Fresh embedded Tier 2 installs use two explicit Helm commands.
- The operator version is pinned and independently auditable.
- The v1.5.1 release must publish the pinned PostgreSQL + AGE image
  before its Helm chart becomes available.
- Several KubeAtlas releases can share one operator.
- Application uninstall is non-destructive to database state by
  default.
- Operator upgrades and database backups remain CloudNativePG
  lifecycle decisions rather than being hidden inside a KubeAtlas
  patch release.

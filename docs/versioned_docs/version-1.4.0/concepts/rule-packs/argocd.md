---
sidebar_position: 3
title: Argo CD
---

# Argo CD rule pack

The `argocd` pack models the GitOps delivery relationships the
`argoproj.io` API group adds.

- **OCI artifact:** `oci://ghcr.io/lithastra/rules/argocd:0.1.0`
- **Requires:** KubeAtlas ≥ 1.1.0

## Modules

| Module | Matched kind | Edges |
|--------|--------------|-------|
| `application` | `Application` | `BELONGS_TO_PROJECT`, `DEPLOYS_TO`, `SOURCED_FROM` |
| `applicationset` | `ApplicationSet` | `BELONGS_TO_PROJECT`, `DEPLOYS_TO`, `SOURCED_FROM` |
| `appproject` | `AppProject` | `ALLOWS_DESTINATION` |

## Edges

- **`BELONGS_TO_PROJECT`** — an Application or ApplicationSet to the
  AppProject named by `spec.project`.
- **`DEPLOYS_TO`** — to the Namespace named by
  `spec.destination.namespace`.
- **`SOURCED_FROM`** — to a `GitRepo` node named by each
  `spec.source` / `spec.sources[]` `repoURL`.
- **`ALLOWS_DESTINATION`** — an AppProject to each Namespace in
  `spec.destinations[]`.

`GitRepo` is a **synthetic node kind** — a git URL is not a
Kubernetes resource, but surfacing it lets the graph show which
Applications share a repository.

## Notes

- An ApplicationSet derives the same edges from its
  `spec.template.spec` Application template; values carrying a
  `{{ }}` generator placeholder are skipped.
- The AppProject is resolved in the Application's own namespace —
  the standard install keeps both in the `argocd` namespace.
- Generated Applications are not modelled: their names come from
  generators and are not statically knowable.

## Out of scope

`spec.destinations[].namespace` globs and negations (`*`,
`prod-*`, `!kube-system`); repository and cluster credential
Secrets.

## Try it

```bash
kubeatlas rules-test --pack=oci://ghcr.io/lithastra/rules/argocd:0.1.0
```

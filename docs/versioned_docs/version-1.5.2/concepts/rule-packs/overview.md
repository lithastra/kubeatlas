---
sidebar_position: 1
title: Rule pack catalogue
---

# Rule pack catalogue

A **rule pack** is a versioned bundle of Rego modules the KubeAtlas
engine loads to derive graph edges from CRDs. See
[Rego rule packs](../rego-rules.md) for how the engine loads and
evaluates them; this page is the catalogue of packs the project
publishes.

Every pack ships as an OCI artifact under
`ghcr.io/lithastra/rules/<name>` and is versioned independently of
the KubeAtlas binary.

## Platform add-on packs

These cover the CRDs a managed Kubernetes platform or core add-on
injects:

| Pack | Covers |
|------|--------|
| `eks` | AWS Load Balancer Controller, Karpenter, EKS Pod Identity |
| `aks` | AKS pod-managed identity and workload identity |
| `gke` | GKE Ingress, Multi-cluster Services, Backup for GKE, Fleet |
| `openshift` | Route, DeploymentConfig, BuildConfig, ImageStream, SCC |
| `cert-manager` | Certificate → Secret and Issuer chains |

## Community packs

These cover popular ecosystem operators:

| Pack | API group | Edge types |
|------|-----------|------------|
| [`istio`](./istio.md) | `networking.istio.io` | `ROUTES_TO`, `BINDS_GATEWAY`, `CONFIGURES`, `USES_TLS_SECRET` |
| [`argocd`](./argocd.md) | `argoproj.io` | `BELONGS_TO_PROJECT`, `DEPLOYS_TO`, `SOURCED_FROM`, `ALLOWS_DESTINATION` |
| [`knative`](./knative.md) | `serving.knative.io` | `CREATES_CONFIGURATION`, `CREATES_REVISION` |
| [`strimzi`](./strimzi.md) | `kafka.strimzi.io` | `MANAGES`, `BELONGS_TO_CLUSTER` |
| [`velero`](./velero.md) | `velero.io` | `STORED_IN`, `USES_SNAPSHOT_LOCATION`, `RESTORES_FROM` |
| [`tekton`](./tekton.md) | `tekton.dev` | `USES_TASK`, `RUNS_PIPELINE`, `RUNS_TASK` |

## Soundness

Every pack derives edges that are **soundly derivable from a single
resource** — a field, an annotation, a label, or a fixed naming
convention on the resource being evaluated. Packs never guess: an
ambiguous or external reference derives no edge rather than a
misleading one. Each pack's page documents its specific resolution
rules and what it leaves out of scope.

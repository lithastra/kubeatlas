---
sidebar_position: 7
title: Amazon EKS
---

# Installing on Amazon EKS

KubeAtlas runs on EKS the same way it runs on any other Kubernetes
distribution — the chart is unchanged, and the read-only RBAC the
chart ships needs no IAM elevation beyond a normal worker-node
identity. What EKS adds is the optional **eks rule pack**: a Rego
pack that models the custom resources EKS add-ons inject into the
cluster.

## What the eks rule pack does (and does not)

EKS add-ons — AWS Load Balancer Controller, Karpenter, the EKS Pod
Identity Agent — install CRDs and create custom resources that sit
*inside* the cluster. The eks pack derives the edges between those
resources and the workloads they back:

| CRD | Edge | Meaning |
|---|---|---|
| `TargetGroupBinding` (`elbv2.k8s.aws`) | `ROUTES_TO` → Service | Which Service the ALB/NLB target group routes to |
| `NodePool` (`karpenter.sh`) | `USES_NODE_CLASS` → `EC2NodeClass` | Which node template Karpenter provisions from |
| `PodIdentityAssociation` (`eks.amazonaws.com`) | `BINDS_PLATFORM_IDENTITY` → ServiceAccount | Which ServiceAccount receives the EKS Pod Identity role |

`ROUTES_TO` is the same edge type the OpenShift pack emits for
`Route` → `Service`: the semantics are isomorphic, and reusing it
keeps the cluster-view legend small.

The pack models the **Kubernetes view** of the cluster. It does
**not** model AWS cloud resources — there are no nodes for S3
buckets, RDS instances, IAM roles, SQS queues, or Lambda functions.
ARNs that appear in CRD specs are carried as plain metadata
properties; KubeAtlas never resolves them through the AWS SDK
(invariant 2.7). If you need a cloud-control-plane graph, that is a
different tool — KubeAtlas reflects what the API server knows.

## Recommended add-on pairings

The pack is most useful on clusters that run these add-ons; install
order does not matter, the pack derives edges from whatever CRs
exist:

- **AWS Load Balancer Controller** — `TargetGroupBinding` →
  Service edges turn an opaque ALB into a traceable path from the
  load balancer to the Pods behind it. Pairs with the
  [ALB Ingress page](./ingress-alb.md).
- **Karpenter** — `NodePool` → `EC2NodeClass` edges let blast-radius
  show which workloads land on which provisioning template.
- **EKS Pod Identity Agent** — `PodIdentityAssociation` →
  ServiceAccount edges show which workloads carry an AWS identity.
  (IRSA — the older `eks.amazonaws.com/role-arn` ServiceAccount
  annotation — is surfaced by a built-in extractor, not this pack.)

## Install

1. **Pick a storage tier.** Tier 1 (in-memory) is fine for a first
   look. For a cluster you will keep watching, use Tier 2 so the
   graph survives Pod restarts and you get [snapshots](../concepts/snapshots.md):

   ```bash
   helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
     --version 1.4.0 \
     --namespace kubeatlas --create-namespace \
     --set persistence.enabled=true \
     --set persistence.embedded.enabled=true \
     --set persistence.embedded.storageSize=4Gi \
     --set rulePacks.extras='{oci://ghcr.io/lithastra/rules/eks:0.1.0}'
   ```

   The eks pack is **opt-in** — it is never auto-detected or
   embedded (invariant 2.3). You load it explicitly through
   `rulePacks.extras`, the same OCI mechanism any third-party pack
   uses. Pinning a semver tag is mandatory; `:latest` trips the
   loader's anti-pattern guard.

2. **Confirm the pack loaded:**

   ```bash
   kubectl logs -n kubeatlas deploy/kubeatlas | grep 'rule pack loaded'
   kubectl exec -n kubeatlas deploy/kubeatlas -- \
     wget -qO- localhost:8080/metrics | grep kubeatlas_rego_modules_loaded
   ```

3. **Smoke-test an edge.** With AWS Load Balancer Controller
   running, find a `TargetGroupBinding` and walk its outgoing
   edges:

   ```bash
   kubectl port-forward -n kubeatlas deploy/kubeatlas 8080:8080 &
   curl -s "http://127.0.0.1:8080/api/v1/resources/<ns>/TargetGroupBinding/<name>/outgoing" | jq .
   ```

   The response carries a `ROUTES_TO` edge to the backing
   Service — that is the eks pack at work.

## Fargate

KubeAtlas runs on Fargate-only EKS clusters without changes. The
chart's Pod requests fit a `0.5 vCPU / 1 GB` Fargate profile for
Tier 1; Tier 2 with the embedded CloudNativePG sub-chart needs a
profile that admits the database Pod's larger request — size it
from `persistence.embedded.storageSize` and the CNPG resource
defaults.

## Troubleshooting

The pack failed to load:

- `rulePacks.extras` entries must be semver-pinned OCI refs. A
  missing tag, a `:latest` tag, or an unreachable registry is fatal
  at startup by design — check `kubectl describe pod`.
- The eks pack declares `kubeatlas: ">= 1.1.0"`. On an older
  KubeAtlas the loader refuses it. Upgrade the chart first.

An edge never appears:

- The pack derives edges only from CRs that exist. No
  `TargetGroupBinding` objects means no `BINDS_TARGET` edges — that
  is correct, not a bug.
- `kubectl logs deploy/kubeatlas | grep rego` — module evaluation
  errors are logged per resource.
- The CRD must be installed *and discoverable*. KubeAtlas picks up
  CRDs at startup; if you installed the add-on after KubeAtlas,
  restart the Pod with
  `kubectl rollout restart deploy/kubeatlas -n kubeatlas`.

---
sidebar_position: 5
title: AWS ALB
---

# AWS Load Balancer Controller (ALB)

For EKS clusters running the
[`aws-load-balancer-controller`](https://kubernetes-sigs.github.io/aws-load-balancer-controller/),
which provisions an Application Load Balancer per `Ingress` resource.
ALB is uncommon outside EKS — if you're not on EKS, prefer
[F5 NGINX](./ingress-nginx-f5.md) or [Traefik](./ingress-traefik.md).

> **Read first**: [Authentication is your job](./security-warning.md).
> ALB itself has **no authentication** — the example wires Cognito at
> the listener level, but you can substitute any OIDC provider.

## 1. Install the controller

Follow the AWS docs:
[kubernetes-sigs.github.io/aws-load-balancer-controller](https://kubernetes-sigs.github.io/aws-load-balancer-controller/v2.8/deploy/installation/)

The controller needs IRSA permissions to create ALBs — that step is
the part most people get wrong, so cross-check the IAM policy before
moving on.

## 2. Install KubeAtlas with the ALB example values

Apply [`helm/kubeatlas/examples/ingress-alb.yaml`](https://github.com/lithastra/kubeatlas/blob/main/helm/kubeatlas/examples/ingress-alb.yaml):

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 0.1.0 \
  --namespace kubeatlas --create-namespace \
  --values helm/kubeatlas/examples/ingress-alb.yaml
```

The example sets:

- `ingress.enabled: true`
- `ingress.acknowledgeNoBuiltinAuth: true`
- `ingress.className: alb`
- `alb.ingress.kubernetes.io/scheme: internet-facing`
- `alb.ingress.kubernetes.io/target-type: ip`
- HTTPS listener with `ssl-redirect`
- `alb.ingress.kubernetes.io/auth-type: cognito` plus a placeholder
  `auth-idp-cognito` JSON — **replace with your User Pool details**

The example also sets `networkPolicy.enabled: false`. The ALB lives
outside the cluster, so a Pod-level NetworkPolicy doesn't gate it
and leaving the policy on creates a false sense of protection.

## 3. Authentication

If you don't use Cognito, swap the annotation block:

- **OIDC provider** — `alb.ingress.kubernetes.io/auth-type: oidc`
  plus `auth-idp-oidc` JSON. See
  [AWS docs](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/listener-authenticate-users.html#configure-user-authentication-oidc).
- **External provider via separate proxy** — set `auth-type: none`
  here and front the public hostname with Cloudflare Access or a
  similar zero-trust gateway.

Either way, **do not run with `auth-type: none` and no upstream
gate.** ALB's only access control without an auth annotation is the
security group, which is rarely enough.

## Verify

```bash
curl -fsSL https://kubeatlas.example.com/healthz
```

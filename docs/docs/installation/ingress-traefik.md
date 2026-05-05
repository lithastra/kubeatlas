---
sidebar_position: 4
title: Traefik
---

# Traefik

For clusters running [Traefik](https://traefik.io/) v2 or v3 as
their Ingress controller. Traefik is the controller the project's
PetClinic test fixture uses, so this path gets the most exercise in
CI.

> **Read first**: [Authentication is your job](./security-warning.md).
> Enabling Ingress without an auth layer in front of it leaks every
> namespace, ConfigMap, and RBAC binding in your cluster.

## 1. Install the controller

```bash
helm repo add traefik https://traefik.github.io/charts
helm repo update
helm install traefik traefik/traefik \
  -n traefik --create-namespace \
  --set ingressClass.enabled=true \
  --set ingressClass.isDefaultClass=true
```

This creates an `IngressClass` named `traefik`.

## 2. Install KubeAtlas with the Traefik example values

Apply [`helm/kubeatlas/examples/ingress-traefik.yaml`](https://github.com/lithastra/kubeatlas/blob/main/helm/kubeatlas/examples/ingress-traefik.yaml):

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 0.1.0 \
  --namespace kubeatlas --create-namespace \
  --values helm/kubeatlas/examples/ingress-traefik.yaml
```

The example sets:

- `ingress.enabled: true`
- `ingress.acknowledgeNoBuiltinAuth: true`
- `ingress.className: traefik`
- Traefik-flavoured annotations (HTTPS entrypoint, redirect)

Edit the `hosts` and `tls` blocks to match your domain.

## 3. Add an authentication layer

Traefik's [`ForwardAuth`](https://doc.traefik.io/traefik/middlewares/http/forwardauth/)
middleware is the cleanest fit. Deploy oauth2-proxy in-cluster, then
attach the middleware via annotation:

```yaml
ingress:
  annotations:
    traefik.ingress.kubernetes.io/router.middlewares: "auth-oauth2-proxy@kubernetescrd"
```

…where `auth-oauth2-proxy` is a `Middleware` CR pointing at your
oauth2-proxy Service. Pomerium and Cloudflare Access are equally
valid alternatives — see the
[security warning](./security-warning.md) for the trade-offs.

## Verify

```bash
curl -fsSL https://kubeatlas.example.com/healthz
```

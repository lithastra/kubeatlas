---
sidebar_position: 3
title: F5 NGINX Ingress
---

# F5 NGINX Ingress Controller

For clusters running the **F5 NGINX Ingress Controller**
(`nginxinc/kubernetes-ingress`) — actively maintained, NGINX-based,
annotation prefix `nginx.org/*`. Not to be confused with the
community `kubernetes/ingress-nginx` project, which is being retired
in March 2026 (see [why no example](./ingress-nginx-eol-notice.md)).

> **Read first**: [Authentication is your job](./security-warning.md).
> Enabling Ingress without an auth layer in front of it leaks every
> namespace, ConfigMap, and RBAC binding in your cluster.

## 1. Install the controller

Follow F5's official chart docs:

```bash
helm repo add nginx-stable https://helm.nginx.com/stable
helm repo update
helm install nginx-ingress nginx-stable/nginx-ingress \
  --namespace nginx-ingress --create-namespace
```

This creates an `IngressClass` named `nginx`.

## 2. Install KubeAtlas with the F5 example values

Apply [`helm/kubeatlas/examples/ingress-nginx-f5.yaml`](https://github.com/lithastra/kubeatlas/blob/main/helm/kubeatlas/examples/ingress-nginx-f5.yaml):

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 0.1.0 \
  --namespace kubeatlas --create-namespace \
  --values helm/kubeatlas/examples/ingress-nginx-f5.yaml
```

The example sets:

- `ingress.enabled: true`
- `ingress.acknowledgeNoBuiltinAuth: true`
- `ingress.className: nginx`
- F5-specific annotations under the `nginx.org/*` prefix

Edit the `hosts` and `tls` blocks to match your domain and TLS
secret before applying.

## 3. Add an authentication layer

The example does **not** wire authentication for you. The most
common pattern with F5 NGINX is `nginx.org/auth-request` pointing at
an in-cluster oauth2-proxy:

```yaml
ingress:
  annotations:
    nginx.org/auth-request: "/oauth2/auth"
    nginx.org/auth-signin: "/oauth2/start?rd=$scheme://$host$request_uri"
```

Deploy oauth2-proxy in the same namespace (or a dedicated `auth`
namespace) and route `/oauth2/*` to it via a separate Ingress rule.
The [oauth2-proxy docs](https://oauth2-proxy.github.io/oauth2-proxy/configuration/integration#configuring-for-use-with-the-nginx-auth_request-directive)
have a full walkthrough.

## Verify

```bash
curl -fsSL https://kubeatlas.example.com/healthz
```

Should return `ok` *after* completing the auth flow in a browser
first (oauth2-proxy will redirect a fresh `curl` to the IdP).

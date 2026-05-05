---
sidebar_position: 2
title: Authentication is your job
---

# Authentication is your job

KubeAtlas v0.1.0 ships with **no built-in authentication**. The chart
defaults reflect this:

- `service.type=ClusterIP` — the API is not reachable from outside the
  cluster.
- `ingress.enabled=false` — exposing the UI is opt-in.
- Flipping `ingress.enabled=true` is rejected by the values schema
  unless you also set `ingress.acknowledgeNoBuiltinAuth=true`.

**This is on purpose.** KubeAtlas reads everything in your cluster:
namespaces, ConfigMaps, Secrets (metadata, not contents), RBAC. An
unauthenticated UI is a cluster-wide read leak.

If you set `ingress.enabled=true` you must front the Ingress with an
authentication layer. Three options that work today:

## oauth2-proxy

Free, widely deployed, sits in front of the Ingress and gates every
request through your IdP (Google, GitHub, OIDC, ...).

- Repository: [github.com/oauth2-proxy/oauth2-proxy](https://github.com/oauth2-proxy/oauth2-proxy)
- Pattern: deploy oauth2-proxy alongside KubeAtlas; configure your
  Ingress controller to route `/` through oauth2-proxy first
  (NGINX: `auth_request`; Traefik: `ForwardAuth` middleware).
- Trade-off: one more component to operate.

## Pomerium

A reverse proxy with policy-based access control built in. Stronger
authorisation story (per-route policies, attribute-based rules) but
heavier than oauth2-proxy.

- Site: [pomerium.com](https://www.pomerium.com/)
- Pattern: route the public hostname for KubeAtlas through Pomerium;
  Pomerium handles AuthN + AuthZ before forwarding to the in-cluster
  Service.
- Trade-off: heavier dependency, but the AuthZ model often replaces a
  separate policy layer.

## Cloudflare Access (or equivalent zero-trust gateway)

Auth happens at the edge, before traffic reaches your cluster. Good
fit if you already use Cloudflare Tunnel or an equivalent
(Tailscale Funnel, Google IAP, AWS Verified Access).

- Site: [Cloudflare Access](https://www.cloudflare.com/zero-trust/products/access/)
- Pattern: expose KubeAtlas via a Tunnel; gate the public hostname
  with an Access policy. The cluster never gets unauthenticated
  traffic.
- Trade-off: vendor lock-in to the chosen edge provider.

## Quick comparison

| Option | Where it runs | Identity | Operational cost |
|---|---|---|---|
| oauth2-proxy | In-cluster, alongside Ingress | OIDC / OAuth2 IdP | Low — one extra Deployment |
| Pomerium | In-cluster, in front of Ingress | OIDC + per-route policy | Medium — replaces some other policy tooling |
| Cloudflare Access | Edge | Cloudflare-managed | Low — but vendor coupling |

There is **no fourth, easier option.** If you can't pick one of these
or an equivalent zero-trust gateway, leave `ingress.enabled=false`
and use `kubectl port-forward` for ad-hoc access.

## What KubeAtlas does *not* do

- Issue or validate tokens.
- Filter API responses based on the caller.
- Audit who looked at what.

Treat KubeAtlas like an internal admin dashboard: anyone who can hit
the URL can see every node and edge in the cluster.

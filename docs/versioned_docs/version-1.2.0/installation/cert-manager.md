---
sidebar_position: 6
title: TLS via cert-manager
---

# TLS via cert-manager

The chart ships an opt-in cert-manager integration for installs
that want managed TLS without a separate `kubectl apply` of
`Certificate` / `Issuer`. It is **off by default** — operators who
prefer to bring their own Secret continue to set `ingress.tls`
verbatim and the chart leaves it alone.

## Prerequisites

cert-manager is a hard runtime prerequisite when the integration
is enabled. The chart does **not** declare it as a sub-chart
dependency on purpose: many environments install cert-manager
cluster-wide (often pre-production), and bundling it would
either duplicate or conflict with that install. Confirm
cert-manager is up before enabling:

```bash
kubectl get crd certificates.cert-manager.io
kubectl get pods -n cert-manager
```

If the CRDs are missing, `helm install` succeeds but the
Certificate stays Pending forever — the NOTES output prints a
verification command you can run to catch this immediately.

## Modes

Three issuer modes cover the common cases. A fourth — `custom` —
is the escape hatch for everything else.

### `selfsigned` (development only)

The chart creates a SelfSigned `Issuer` and a `Certificate` that
references it. The resulting Secret is wired into the Ingress's
`spec.tls`. Browsers warn; `curl -k` works. Not for production.

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.2.0 \
  --namespace kubeatlas --create-namespace \
  --set ingress.enabled=true \
  --set ingress.acknowledgeNoBuiltinAuth=true \
  --set ingress.hosts[0].host=kubeatlas.local \
  --set ingress.certManager.enabled=true \
  --set ingress.certManager.issuer=selfsigned
```

### `letsencrypt-staging` / `letsencrypt-prod`

The chart creates a `Certificate` only. It does **not** create the
ACME `ClusterIssuer` — that's intentional, because ACME issuers
typically already exist cluster-wide and rolling our own would
either duplicate or conflict with the operator's setup.

The chart references a `ClusterIssuer` named exactly
`letsencrypt-staging` or `letsencrypt-prod` (matching the value
of `ingress.certManager.issuer`). Override the name with
`ingress.certManager.issuerName` if your existing ClusterIssuer
uses a different one.

```bash
# 1. Bring your own ClusterIssuer (one-time, cluster-wide).
kubectl apply -f - <<'EOF'
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: ops@example.com
    privateKeySecretRef:
      name: letsencrypt-prod-account
    solvers:
      - http01:
          ingress:
            class: nginx
EOF

# 2. Helm install with cert-manager mode.
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.2.0 \
  --namespace kubeatlas --create-namespace \
  --set ingress.enabled=true \
  --set ingress.acknowledgeNoBuiltinAuth=true \
  --set ingress.hosts[0].host=kubeatlas.example.com \
  --set ingress.certManager.enabled=true \
  --set ingress.certManager.issuer=letsencrypt-prod
```

Start with `letsencrypt-staging` to avoid the production rate
limit while you debug DNS / firewall / solver issues.

### `custom`

Reference any (Cluster)Issuer the operator already has — corporate
PKI, HashiCorp Vault PKI, custom ACME, etc. Schema requires an
explicit `issuerName`; `issuerKind` defaults to `ClusterIssuer`
but can be overridden to `Issuer` when the issuer is namespaced.

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.2.0 \
  --namespace kubeatlas --create-namespace \
  --set ingress.enabled=true \
  --set ingress.acknowledgeNoBuiltinAuth=true \
  --set ingress.hosts[0].host=kubeatlas.example.com \
  --set ingress.certManager.enabled=true \
  --set ingress.certManager.issuer=custom \
  --set ingress.certManager.issuerName=corp-ca \
  --set ingress.certManager.issuerKind=ClusterIssuer
```

### Bring-your-own Secret

The chart's `ingress.tls` array — the v0.1.0-era path — still
works unchanged. The schema enforces that `ingress.tls` and
`ingress.certManager.enabled=true` are mutually exclusive, so
mixing modes fails fast at `helm install` time rather than
silently ignoring one.

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 1.2.0 \
  --namespace kubeatlas --create-namespace \
  --set ingress.enabled=true \
  --set ingress.acknowledgeNoBuiltinAuth=true \
  --set 'ingress.tls[0].secretName=my-tls-secret' \
  --set 'ingress.tls[0].hosts[0]=kubeatlas.example.com'
```

## Verifying

After `helm install`, NOTES.txt prints a `kubectl wait` invocation
the operator can run to confirm the Certificate becomes Ready.
Roughly:

```bash
kubectl wait -n kubeatlas certificate/kubeatlas \
  --for=condition=Ready --timeout=2m
```

ACME issuers can take longer than 2 minutes when the cert-manager
operator is bootstrapping the ACME account; bump `--timeout` to
5–10 minutes for first-time installs.

## Troubleshooting

Certificate stays in `Pending`:

```bash
kubectl describe certificate kubeatlas -n kubeatlas
kubectl describe certificaterequest -n kubeatlas
```

ACME order failing on HTTP-01:

- The Ingress controller must be reachable from the public
  internet on port 80; any cloud LB / firewall in front needs to
  forward `/.well-known/acme-challenge/...` paths.
- Check the `Order` resource for the exact failure reason.

Selfsigned certificate browser warning:

- Expected. `selfsigned` mode is documented as development-only;
  use `custom` with a corporate CA for non-public production, or
  ACME for public hostnames.

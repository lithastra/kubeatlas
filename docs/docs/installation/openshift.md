---
sidebar_position: 5
title: OpenShift / OpenShift Local (CRC)
---

# Installing on OpenShift

KubeAtlas runs on OpenShift the same way it runs on any Kubernetes
distribution — the chart is unchanged. What you get for free on
OpenShift:

- The OpenShift detector at startup notices `route.openshift.io` and
  auto-loads the embedded openshift rule pack (Route, DeploymentConfig,
  BuildConfig, ImageStream, SecurityContextConstraints).
- The chart's RBAC stays read-only, so it does not need any special
  cluster-admin elevation beyond what the install action already
  grants.

This page covers the two cases users actually hit: CRC (OpenShift
Local on a laptop, used for development and CI) and a real OCP 4.x
cluster.

## OpenShift Local (CRC)

CRC ships a single-node OpenShift cluster. It needs a Red Hat
pull-secret from
[console.redhat.com/openshift/install/pull-secret](https://console.redhat.com/openshift/install/pull-secret).

1. **Install CRC** (≥ 2.40 — the pull-secret handling changed in
   2.40 and we don't validate older versions):

   ```bash
   # macOS
   brew install crc
   # Linux
   curl -fL https://developers.redhat.com/content-gateway/file/pub/openshift-v4/clients/crc/latest/crc-linux-amd64.tar.xz | tar -xJ
   sudo install crc-linux-*/crc /usr/local/bin/
   ```

2. **Configure and start**:

   ```bash
   crc config set pull-secret-file ~/Downloads/pull-secret.txt
   crc config set memory 12288
   crc config set cpus 6
   crc config set disk-size 50
   crc start
   eval "$(crc oc-env)"
   ```

3. **Install KubeAtlas**:

   ```bash
   helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
     --version 0.1.0 \
     --namespace kubeatlas --create-namespace \
     --set persistence.enabled=true \
     --set persistence.embedded.enabled=true \
     --set persistence.embedded.storageSize=4Gi \
     --set rulePacks.openshift=auto
   ```

   `rulePacks.openshift=auto` is the default. The detector will fire
   at startup and emit a log line you can grep for:

   ```bash
   oc logs -n kubeatlas deploy/kubeatlas | grep 'OpenShift API group detected'
   ```

4. **Smoke-test**:

   Apply any Route in any namespace, wait ~30 seconds, then walk its
   outgoing edges:

   ```bash
   oc port-forward -n kubeatlas deploy/kubeatlas 8080:8080 &
   curl -s "http://127.0.0.1:8080/api/v1alpha1/resources/<ns>/Route/<name>/outgoing" | jq .
   ```

   The response includes a `ROUTES_TO` edge to the backing Service —
   that's the openshift rule pack at work.

## Production OCP 4.x

The chart's `securityContext` already targets the
`restricted-v2` SCC, which is the OpenShift 4.11+ default for
non-privileged workloads. The chart does not request any
`anyuid`-class SCC binding; install in any namespace where the
default project SCC permits the restricted pod-spec the chart
emits.

The single OpenShift-specific knob worth surfacing for OCP is image
sourcing. If your cluster pulls from a mirror, override
`image.repository` to that mirror's path:

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas \
  --version 0.1.0 \
  --namespace kubeatlas --create-namespace \
  --set image.repository=registry.example.com/mirror/lithastra/kubeatlas \
  --set image.tag=v0.1.0
```

If you intend to expose the UI via an OCP Route, the chart does not
manage Routes itself (it is platform-neutral). Create one
post-install:

```yaml
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: kubeatlas
  namespace: kubeatlas
spec:
  to: { kind: Service, name: kubeatlas }
  port: { targetPort: 8080 }
  tls: { termination: edge }
```

## CI integration

The repo ships a `.github/workflows/e2e-openshift-local.yml` workflow
that runs the same install + verification flow against a fresh CRC
cluster every Monday at 04:00 UTC. It is also reachable via
`workflow_dispatch` and on tag pushes. It needs a
`CRC_PULL_SECRET` repo secret; without it the job short-circuits
with a notice. Failed scheduled runs auto-open or update an issue
labelled `ci, openshift`.

## Troubleshooting

The pack failed to load:

- The detector decides per-startup. If you installed before the
  cluster exposed `route.openshift.io` (rare, but possible during
  cluster bootstrap), restart the kubeatlas Pod with
  `oc rollout restart deploy/kubeatlas -n kubeatlas`.
- To force-load the pack regardless of what discovery says, set
  `--set rulePacks.openshift=true`. To never load it (e.g. you
  ship a fork via `rulePacks.extras`), set `false`.

ROUTES_TO never appears:

- `kubectl logs deploy/kubeatlas | grep rego` — modules loaded?
- `curl /metrics | grep kubeatlas_rego_modules_loaded` — non-zero?
- The rule pack expects `spec.to.kind == "Service"`. Routes that
  point at non-Service backends (rare) won't yield an edge.

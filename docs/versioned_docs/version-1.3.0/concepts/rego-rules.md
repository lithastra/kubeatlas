---
sidebar_position: 4
title: Rego rule packs
---

# Rego rule packs

KubeAtlas v1.0 ships with eight built-in edge types (OWNS, USES_CONFIGMAP, …) that cover everything in the core Kubernetes API. Edges that depend on CRDs — Route → Service in OpenShift, Certificate → Secret in cert-manager, and similar — come from **Rego rule packs** loaded at startup.

This page is the contract between the rule-pack authors in `lithastra/kubeatlas-rules` and the engine in `pkg/extractor/rego`.

## How rule packs flow into the engine

```
┌──────────────┐    ┌─────────────────┐    ┌─────────────┐
│ rule pack    │ →  │ pkg/extractor   │ →  │ pkg/crd     │
│ (.rego +     │    │ /rego (engine)  │    │ (per-CRD    │
│  metadata)   │    │  Router + Cache │    │  informer)  │
└──────────────┘    └─────────────────┘    └─────────────┘
                            ↑                     │
                            └─── Resource events──┘
                                 derived edges
                                       │
                                       ▼
                                ┌─────────────┐
                                │ GraphStore  │
                                └─────────────┘
```

Three pieces:

1. **Loader** (`pkg/extractor/rego/loader.go`): reads `metadata.yaml` + every referenced `.rego` file from a directory or OCI artifact. Validates `rego_api: v1` and the `kubeatlas: ">= 1.0.0"` semver constraint at load time — packs that don't match are rejected with a typed sentinel and skipped (the engine keeps running).
2. **Engine** (`engine.go` + `cache.go` + `router.go`): compiles each module via OPA's `PrepareForEval`, routes per-resource events through a `(Group, Kind)` table to the matching modules, and caches results keyed on `(UID, ResourceVersion, RuleHash)`.
3. **CRD discovery** (`pkg/crd`): at startup AND at runtime, watches `apiextensions.k8s.io/v1/CustomResourceDefinition` and spins up one dynamic informer per CRD whose objects flow through the engine.

## CRDs come and go at runtime

KubeAtlas does **not** require a restart when a CRD shows up. The CRD informer reacts to add/update events the moment the API server publishes a new CRD, registers the per-CRD informer, and starts feeding instances into the rego engine. A typical sequence:

```bash
helm install kubeatlas oci://ghcr.io/lithastra/charts/kubeatlas
# kubeatlas Pod up, no cert-manager yet → 0 cert-manager rules touched

helm install cert-manager jetstack/cert-manager --namespace cert-manager --create-namespace
# cert-manager installs its CRDs. KubeAtlas log within seconds:
#   INFO Discovered CRD, registered informer gvr=cert-manager.io/v1/certificates kind=Certificate

kubectl apply -f my-certificate.yaml
# A few seconds later:
#   GET /api/v1alpha1/resources/<ns>/Certificate/<name> returns the resource
#   GET /api/v1alpha1/resources/<ns>/Secret/<linked-secret>/incoming includes
#       the cert-manager STORES_IN edge (rego-derived).
```

If the CRD is later deleted, KubeAtlas logs `INFO Deregistered CRD informer ...` and stops watching that resource. Existing graph entries are left in place — operators sometimes delete a CRD by accident and re-create it, and yanking the graph would mean losing diagnostic context.

## Writing a rule pack

A pack is a directory with one `metadata.yaml` and any number of `.rego` modules:

```
my-pack/
├── metadata.yaml
├── certificate.rego
├── issuer.rego
└── tests/
    └── certificate_test.rego
```

`metadata.yaml`:

```yaml
name: cert-manager
version: 1.0.0
rego_api: v1                  # KubeAtlas Rego interface contract
kubeatlas: ">= 1.0.0"         # semver constraint against the engine
description: "cert-manager CRDs"
modules:
  - name: certificate
    file: certificate.rego
    entrypoint: data.kubeatlas.cert_manager.certificate.derive
    match:
      group: cert-manager.io
      kind: Certificate
```

`certificate.rego` (Rego v1 syntax):

```rego
package kubeatlas.cert_manager.certificate

import rego.v1

derive contains edge if {
    input.kind == "Certificate"
    input.spec.secretName != ""
    edge := {
        "type": "STORES_IN",
        "from": {
            "kind": "Certificate",
            "namespace": input.metadata.namespace,
            "name": input.metadata.name,
        },
        "to": {
            "kind": "Secret",
            "namespace": input.metadata.namespace,
            "name": input.spec.secretName,
        },
    }
}
```

The output is a Rego **set** of edge maps. Each edge has `type` (string), `from` and `to` (each `{kind, namespace, name}`). Anything else is ignored / treated as a shape error.

## Input shape (Rego v1 API)

Every module receives a JSON-like map shaped like:

```json
{
  "kind": "Certificate",
  "apiVersion": "cert-manager.io/v1",
  "metadata": {
    "namespace": "demo",
    "name": "my-cert",
    "uid": "...",
    "labels": { },
    "annotations": { },
    "resourceVersion": "1234"
  },
  "spec": {
    "secretName": "my-cert-tls",
    "issuerRef": { "kind": "ClusterIssuer", "name": "letsencrypt" }
  }
}
```

`spec` carries the unstructured object's spec block exactly as the API server returned it — extractors can read any nested field without KubeAtlas knowing the schema in advance.

## What you cannot do

The engine enforces guardrails at evaluation time:

- **CPU budget**: every evaluation runs under a 100 ms default timeout (clamped to `[50ms, 1s]`). A runaway rule returns `ErrEvalTimeout`, gets counted in `kubeatlas_rego_eval_timeout_total`, and is skipped for that resource — it does not stall the informer pipeline.
- **Panic isolation**: if the OPA runtime panics inside a rule, it is recovered and surfaced as `ErrEvalPanic` + counted in `kubeatlas_rego_eval_panic_total`. The server keeps running.
- **No state**: rules cannot read external data, write files, or call HTTP. Only `input`.

## Loading extra packs from OCI

The OpenShift pack ships embedded in the kubeatlas binary, but every other pack lives in [`lithastra/kubeatlas-rules`](https://github.com/lithastra/kubeatlas-rules) and is published as an OCI artifact under `ghcr.io/lithastra/rules/<pack>:<version>`. Operators load them via Helm or the `--rule-pack` flag.

### Helm

```yaml
# helm values.yaml
rulePacks:
  openshift: auto       # detect + load embedded pack
  extras:
    - oci://ghcr.io/lithastra/rules/cert-manager:0.1.0
    - oci://ghcr.io/lithastra/rules/argo:0.2.0
```

The chart writes a comma-separated `KUBEATLAS_RULE_PACKS` env var; the binary reads it at startup and pulls each artifact via the same OCI flow `docker pull` uses (Docker credential helpers honored — `docker login ghcr.io` first if the artifact is private).

### CLI

```bash
kubeatlas \
  --rule-pack oci://ghcr.io/lithastra/rules/cert-manager:0.1.0 \
  --rule-pack ./local/dev-pack
```

`--rule-pack` is repeatable. Local directories work too — useful when iterating on a pack you have not pushed yet.

### Pinning

Tags must be a real semver. The loader rejects `:latest` with an error so a registry mutation cannot silently change the rules a deployment loads.

## Testing a pack locally

`kubeatlas rules-test` evaluates a pack against a directory of YAML samples without needing a cluster:

```bash
# Local pack directory
kubeatlas rules-test --pack=./cert-manager

# OCI artifact
kubeatlas rules-test \
  --pack=oci://ghcr.io/lithastra/rules/cert-manager:0.1.0 \
  --samples=./cert-manager/samples

# Machine-readable output for CI
kubeatlas rules-test --pack=./cert-manager --format=json
```

The exit code is 0 only if every sample produced at least one edge. CI in `lithastra/kubeatlas-rules` invokes this through `make integration` so every PR exercises the pack against the real engine.

## Versioning

`rego_api: v1` is a contract: KubeAtlas v1.x guarantees the shape above. A future `rego_api: v2` will only ship after at least 6 months of dual-version support — see [API versioning](./api-versioning.md). If you set `kubeatlas: ">= 2.0.0"` in a pack and try to load it on a 1.x binary, the loader rejects it with `ErrIncompatibleKubeAtlas` and the rest of the engine keeps running.

---
sidebar_position: 4
title: Developer Guide
---

# Developer Guide

This guide is for contributors. If you only want to *use* KubeAtlas,
start with the [Quick Start](./quick-start.md) instead.

## Prerequisites

- **Go 1.26 or later** — KubeAtlas uses `log/slog`, generics, and the
  Go 1.22+ `net/http` `ServeMux` features (the latter from Phase 1).
- **A Kubernetes cluster you control** — any cluster works for the
  end-to-end flow. If you don't already have one, install
  [Docker](https://docs.docker.com/get-docker/) +
  [kind](https://kind.sigs.k8s.io/); the docs use kind as the example
  because it's the fastest local path.
- **kubectl** — at the same minor version as your cluster.
- **`golangci-lint`** *(optional locally; required in CI)* —
  `brew install golangci-lint` or see [installation docs](https://golangci-lint.run/).
- **`setup-envtest`** *(optional)* — needed only to run the informer
  integration tests locally. Without it, those tests skip with a
  clear message.

```bash
# Install the envtest binaries once.
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
export KUBEBUILDER_ASSETS=$(setup-envtest use 1.30.x -p path)
```

## Repository layout

```
kubeatlas/
├── cmd/kubeatlas/        # CLI entry point (-once or watch)
├── pkg/
│   ├── graph/            # Resource / Edge / GraphStore types
│   │   └── storetest/    # Reusable contract test suite
│   ├── store/
│   │   ├── memory/       # Tier 1 in-memory backend (default)
│   │   └── postgres/     # Tier 2 placeholder, enabled in v1.0
│   ├── discovery/        # K8s client + informer + GVR registry
│   ├── extractor/        # Edge extractors (Phase 0 W4)
│   ├── aggregator/       # Pre-aggregated views (Phase 0 W4)
│   └── api/              # REST + WebSocket (Phase 1 W5+)
├── web/                  # Frontend (Phase 1 W6+)
├── helm/                 # Helm Chart (Phase 1 W10+)
├── docs/                 # Docusaurus site
└── test/
    ├── petclinic/        # Reference fixture
    └── verify/           # End-to-end verification scripts
```

## Build & test

```bash
# Compile everything
go build ./...

# Unit tests with coverage
go test ./... -coverprofile=cover.out
go tool cover -func=cover.out | tail -1

# Integration tests (informer + envtest)
KUBEBUILDER_ASSETS=$(setup-envtest use 1.30.x -p path) go test ./pkg/discovery/...

# Lint
golangci-lint run ./...

# End-to-end on a real cluster (kind is one option; any K8s cluster you control works)
test/petclinic/run.sh base

# Generate the three snapshots the verifier reads (must be run before phase0.sh)
go run ./cmd/kubeatlas/ -once                                                  > /tmp/graph-resource.json
go run ./cmd/kubeatlas/ -once -level=cluster                                   > /tmp/graph-cluster.json
go run ./cmd/kubeatlas/ -once -level=namespace -namespace=petclinic            > /tmp/graph-namespace.json

bash test/verify/phase0.sh
```

## Two run modes

The CLI has two modes today:

| Flag | Behaviour |
|---|---|
| `-once` | Walk every API resource the cluster exposes, build the graph, write JSON to stdout and DOT to `output/kubeatlas.dot`, exit. The PoC-era path; useful for one-off dumps. |
| *(default)* | Start an informer that streams add/update/delete events into the in-memory store and run until `Ctrl-C`. There is no API surface yet (see Phase 1 for that), so this mode is mostly useful for development and for verifying the watch pipeline is healthy. |

## Adding a new edge type — a worked example

Suppose you want to add `EdgeTypeMountsCSIDriver` (a Pod's CSI volume
references a `CSIDriver` cluster-scoped resource). Walk through this
end-to-end so you have a feel for how a contribution lands.

### 1. Declare the type

Edit `pkg/graph/model.go`:

```go
const (
    // ... existing constants
    EdgeTypeMountsCSIDriver EdgeType = "MOUNTS_CSI_DRIVER"
)

var AllEdgeTypes = []EdgeType{
    // ... existing types
    EdgeTypeMountsCSIDriver,
}
```

### 2. Make the resource visible

If the `to` end of your edge is a kind KubeAtlas does not yet watch,
add it to `pkg/discovery/resources.go::CoreGVRs`. CSIDrivers live at
`storage.k8s.io/v1`.

### 3. Write the extractor

Create `pkg/extractor/csidriver.go` (interface lands in P0-T15):

```go
package extractor

func (r *Registry) extractCSIDrivers(pod graph.Resource, all []graph.Resource) []graph.Edge {
    // 1. Read pod.Spec.Volumes for csi.driver references.
    // 2. For each driver name, emit Edge{
    //        From: pod.ID(),
    //        To:   "/CSIDriver/" + name,
    //        Type: graph.EdgeTypeMountsCSIDriver,
    //    }
}
```

Register it in `Registry.Default()`.

### 4. Test it

Create `pkg/extractor/csidriver_test.go`:

```go
func TestCSIDriver_HappyPath(t *testing.T) {
    pod := graph.Resource{ /* ... volumes referencing "ebs.csi.aws.com" ... */ }
    edges := registry.ExtractAll(pod, allResources)
    // Assert exactly one edge with Type == EdgeTypeMountsCSIDriver.
}
```

Aim for ≥ 80% coverage on the new extractor — the project enforces
this via `go tool cover` on `pkg/extractor`.

### 5. Document it

Add a row to the edge-type table in
[`docs/docs/architecture.md`](./architecture.md). One sentence is
enough: "MOUNTS_CSI_DRIVER — Pod → CSIDriver (cluster scope), via
spec.volumes[].csi.driver."

### 6. Open a PR

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(extractor): add MOUNTS_CSI_DRIVER edge

CSI volumes reference a CSIDriver resource at the cluster scope.
Adds the extractor + 5 test cases and updates architecture.md.

Signed-off-by: Your Name <you@example.com>
```

CI will reject the PR if any of these fail:

- `go test ./...` (race-enabled)
- `golangci-lint run ./...`
- The non-ASCII gate (no CJK in `cmd/` or `pkg/`)
- The DCO gate (every commit needs `Signed-off-by`)
- The Conventional Commits gate (PR title and every commit subject)

See the [CONTRIBUTING.md](https://github.com/lithastra/kubeatlas/blob/main/CONTRIBUTING.md)
for the full list of allowed scopes.

## Where to ask questions

- **Bug reports / feature requests** — open a GitHub issue.
- **Design discussions** — start a GitHub Discussion.
- **Security** — see [SECURITY.md](https://github.com/lithastra/kubeatlas/blob/main/SECURITY.md).

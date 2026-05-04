# Contributing to KubeAtlas

We welcome contributions! By contributing, you agree to the
[Developer Certificate of Origin (DCO)](./DCO).

## How to contribute

1. Fork this repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Commit your changes with a sign-off (`git commit -s -m "Add my feature"`)
4. Push to your fork (`git push origin feature/my-feature`)
5. Open a Pull Request

The `-s` flag adds a `Signed-off-by` line, required under our DCO policy.

## Coding & commit conventions

KubeAtlas follows CNCF Sandbox-ready practices.

### Language

- Source code (identifiers, comments, godoc), commit messages, and PR/Issue titles MUST be in English.
- Documentation under `docs/` is English-canonical; translations under `docs/<locale>/` are welcome.

### Commit messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short summary>

<optional body, wrapped at 72 chars>

Signed-off-by: Your Name <you@example.com>
```

Allowed `<type>` values: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `ci`, `build`, `perf`.

Allowed `<scope>` values are enforced by `commitlint.config.js` and currently include:
`graph`, `discovery`, `extractor`, `aggregator`, `store`, `api`, `cmd`, `ci`, `docs`, `helm`, `web`, `repo`, `deps`.

Example:

```
feat(discovery): add informer-based watch loop

Replaces the dynamic-client polling path used in the PoC with a
SharedInformerFactory. Reduces incremental update latency from
~5s to <1s.

Signed-off-by: Random J Developer <random@developer.example.org>
```

### Code style

- **Go**: `gofmt`, `goimports`, and `golangci-lint` (config in `.golangci.yml`).
- **TypeScript / React**: `eslint` + `prettier` (config in `web/`, added in Phase 1).
- New code requires tests; CI enforces coverage non-regression.

## Local development

### Prerequisites

- Go 1.26 or later
- Docker
- [kind](https://kind.sigs.k8s.io/) for local Kubernetes testing
- (Optional) [`golangci-lint`](https://golangci-lint.run/) for local linting (CI runs it on every PR regardless)
- (Optional) [`setup-envtest`](https://book.kubebuilder.io/reference/envtest.html) for the informer integration tests; without it those tests skip cleanly

Install `setup-envtest` and the matching binaries:

```bash
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
export KUBEBUILDER_ASSETS=$(setup-envtest use 1.30.x -p path)
```

### Build & test

```bash
# Build
go build ./cmd/kubeatlas/

# Unit tests
go test ./...

# With coverage
go test ./... -coverprofile=cover.out
go tool cover -html=cover.out

# Integration tests (envtest, no real cluster needed; requires KUBEBUILDER_ASSETS)
go test ./pkg/discovery/...

# End-to-end (requires a kind cluster + PetClinic deployed)
test/petclinic/run.sh base
go run ./cmd/kubeatlas/ -once > /tmp/graph.json
test/verify/phase0.sh
```

### Running the binary

```bash
# One-shot mode (snapshot the cluster, write JSON + DOT, exit)
go run ./cmd/kubeatlas/ -once

# Watch mode (default; informer streams cluster changes into memory until SIGINT)
go run ./cmd/kubeatlas/
```

### Adding a new edge type

A worked example lives in [docs/docs/developer-guide.md](./docs/docs/developer-guide.md). The short version:

1. Add a constant to `pkg/graph/model.go`'s `EdgeType` enum
2. Append it to `AllEdgeTypes`
3. Write an extractor in `pkg/extractor/<name>.go` implementing the `Extractor` interface
4. Register it in `pkg/extractor.Registry.Default()`
5. Add a test in `pkg/extractor/<name>_test.go`
6. Update `docs/docs/architecture.md`

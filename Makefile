# KubeAtlas top-level Makefile.
#
# Conventions:
#   - All targets are .PHONY (no real artifacts under tracked paths).
#   - Targets shell out to the standard toolchains (go, helm, npm)
#     rather than reinventing them; CI runs the same commands.

.PHONY: help build test test-postgres test-short bench-postgres bench-cypher verify-no-cgo changelog-extract

help:
	@awk -F':.*##' '/^[a-zA-Z_-]+:.*##/ {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# build produces a local development binary under bin/. goreleaser
# owns real release versioning; this target exists so a hand-built
# binary can run `rules-test`, which rejects any pack whose
# `kubeatlas: ">= x.y.z"` constraint the running binary fails. A
# plain `go build` leaves Version=="dev" — not valid semver — and
# fails every such check, so we stamp the nearest release tag plus
# +dev build metadata (semver ignores build metadata when comparing,
# so it still satisfies ">= 1.1.0"). Override with
# `make build VERSION=1.2.0`.
TAG     := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION ?= $(if $(TAG),$(TAG),0.0.0)+dev.$(COMMIT)
LDFLAGS := -X github.com/lithastra/kubeatlas/pkg/version.Version=$(VERSION) -X github.com/lithastra/kubeatlas/pkg/version.Commit=$(COMMIT) -X github.com/lithastra/kubeatlas/pkg/version.Date=$(DATE)

build: ## Build the kubeatlas binary into bin/ (CGO-free, version-stamped).
	@mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/kubeatlas ./cmd/kubeatlas
	@echo "built bin/kubeatlas ($(VERSION))"

test: ## Run all Go tests with race detector.
	go test -race ./...

test-short: ## Run Go tests skipping testcontainers (no Docker required).
	go test -short -race ./...

test-postgres: ## Run only the Postgres+AGE testcontainers tests.
	go test -race -run '^Test' ./pkg/store/postgres/...

bench-postgres: ## Run the Upsert + AGE-vs-SQL benchmarks (deterministic, < 5s wall).
	go test -bench=BenchmarkUpsert1000Resources -benchtime=1x -run=^$$ ./pkg/store/postgres/...

bench-cypher: ## Run the AGE-vs-SQL ListOutgoing comparison (200 iterations).
	go test -bench=BenchmarkListOutgoing_AGE_vs_SQL -benchtime=200x -run=^$$ ./pkg/store/postgres/...

# verify-no-cgo enforces invariant 2.2 (Phase 2 guide): the production
# build must always link CGO_ENABLED=0, and no Wasm runtime may sneak
# into the dep graph via a transitive upgrade.
verify-no-cgo: ## Assert CGO_ENABLED=0 builds and no Wasm runtime is in deps.
	@CGO_ENABLED=0 go build -o /dev/null ./...
	@if go list -deps ./... | grep -E '(wasmtime|wasmer|wazero)'; then \
	  echo "FORBIDDEN: Wasm runtime detected in dep graph"; exit 1; \
	fi
	@echo "OK: CGO_ENABLED=0 builds and no Wasm runtime in deps."

# Extract one release's CHANGELOG section into a file goreleaser can
# consume via --release-notes. VERSION is the tag (e.g. v1.4.0). OUT
# defaults to /tmp/release-notes.md so the CI workflow's goreleaser
# step can read it without further plumbing.
#
# Trims the leading "## [vX.Y.Z] — <headline>" line to a plain
# "## <headline>" so it renders cleanly as the release body, and
# stops at the next "## [" header or the reference-link block.
changelog-extract: ## Write the CHANGELOG section for VERSION=vX.Y.Z to OUT (default /tmp/release-notes.md).
	@if [ -z "$(VERSION)" ]; then echo "VERSION=vX.Y.Z required" >&2; exit 1; fi
	@: > "$(or $(OUT),/tmp/release-notes.md)"
	@awk -v want="$(VERSION)" ' \
	  /^## \[v[0-9]+\.[0-9]+\.[0-9]+\]/ { \
	    if (out) { close(out); out = "" } \
	    match($$0, /v[0-9]+\.[0-9]+\.[0-9]+/); \
	    ver = substr($$0, RSTART, RLENGTH); \
	    if (ver == want) { \
	      sub(/^## \[v[0-9]+\.[0-9]+\.[0-9]+\] — /, "## "); \
	      out = "$(or $(OUT),/tmp/release-notes.md)"; \
	      print > out; next \
	    } \
	  } \
	  /^\[v[0-9]+\.[0-9]+\.[0-9]+\]:/ { if (out) { close(out); out = "" } } \
	  out { print > out } \
	' CHANGELOG.md
	@test -s "$(or $(OUT),/tmp/release-notes.md)" || { echo "no CHANGELOG section for $(VERSION)" >&2; exit 1; }
	@echo "wrote $(or $(OUT),/tmp/release-notes.md)"

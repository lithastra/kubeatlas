# KubeAtlas top-level Makefile.
#
# Conventions:
#   - All targets are .PHONY (no real artifacts under tracked paths).
#   - Targets shell out to the standard toolchains (go, helm, npm)
#     rather than reinventing them; CI runs the same commands.

.PHONY: help test test-postgres test-short verify-no-cgo

help:
	@awk -F':.*##' '/^[a-zA-Z_-]+:.*##/ {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: ## Run all Go tests with race detector.
	go test -race ./...

test-short: ## Run Go tests skipping testcontainers (no Docker required).
	go test -short -race ./...

test-postgres: ## Run only the Postgres+AGE testcontainers tests.
	go test -race -run '^Test' ./pkg/store/postgres/...

# verify-no-cgo enforces invariant 2.2 (Phase 2 guide): the production
# build must always link CGO_ENABLED=0, and no Wasm runtime may sneak
# into the dep graph via a transitive upgrade.
verify-no-cgo: ## Assert CGO_ENABLED=0 builds and no Wasm runtime is in deps.
	@CGO_ENABLED=0 go build -o /dev/null ./...
	@if go list -deps ./... | grep -E '(wasmtime|wasmer|wazero)'; then \
	  echo "FORBIDDEN: Wasm runtime detected in dep graph"; exit 1; \
	fi
	@echo "OK: CGO_ENABLED=0 builds and no Wasm runtime in deps."

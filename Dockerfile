# Runtime image. goreleaser cross-compiles the binary natively (with
# -tags embed_web after copying web/dist into cmd/kubeatlas/web/dist
# via the before-hook in .goreleaser.yml) and then invokes `docker
# buildx` against this Dockerfile in a temp context that contains
# exactly two files: this Dockerfile and the `kubeatlas` binary.
#
# The base is debian:bookworm-slim rather than distroless because
# the /api/v1/export endpoint shells out to the graphviz `dot` CLI
# for server-side SVG/PNG rendering (ADR 0012), and distroless has
# no package manager to install it. The container still runs as a
# non-root user and needs no writable filesystem at runtime, so the
# Helm Chart's runAsNonRoot=true / readOnlyRootFilesystem=true
# posture is preserved.
#
# Local `docker build .` does not work with this Dockerfile by
# design — it expects the binary to be in the build context already.
# To produce a Docker image locally, run:
#
#   goreleaser release --snapshot --clean --skip=publish
#
# which performs the native build + docker build in one shot.

FROM debian:bookworm-slim

# graphviz   — the `dot` CLI the export endpoint renders through.
# ca-certificates — TLS trust for the API server and OCI registries
#                   (distroless/static shipped these; slim does not).
# `dot -c` builds the graphviz plugin config into the image so the
# renderer needs no writable filesystem at runtime.
RUN apt-get update \
 && apt-get install -y --no-install-recommends graphviz ca-certificates \
 && dot -c \
 && rm -rf /var/lib/apt/lists/*

# Non-root user with no login shell. UID 65532 matches the
# distroless "nonroot" user the Helm chart's securityContext expects.
RUN useradd --system --uid 65532 --user-group --no-create-home \
      --shell /usr/sbin/nologin nonroot

COPY kubeatlas /kubeatlas

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/kubeatlas"]

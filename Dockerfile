# Runtime-only image. goreleaser cross-compiles the binary natively
# (with -tags embed_web after copying web/dist into
# cmd/kubeatlas/web/dist via the before-hook in .goreleaser.yml) and
# then invokes `docker buildx` against this Dockerfile in a temp
# context that contains exactly two files: this Dockerfile and the
# `kubeatlas` binary. Anything you reference here that isn't in that
# pair must be declared via `extra_files` in .goreleaser.yml.
#
# distroless/static + nonroot matches the Helm Chart's
# runAsNonRoot=true and readOnlyRootFilesystem=true defaults, so
# every install path has the same security posture.
#
# Local `docker build .` does not work with this Dockerfile by
# design — it expects the binary to be in the build context already.
# To produce a Docker image locally, run:
#
#   goreleaser release --snapshot --clean --skip=publish
#
# which performs the native build + docker build in one shot.

FROM gcr.io/distroless/static-debian12:nonroot

COPY kubeatlas /kubeatlas

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/kubeatlas"]

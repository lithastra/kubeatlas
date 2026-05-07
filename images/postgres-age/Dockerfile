# syntax=docker/dockerfile:1
#
# lithastra/postgres-age — KubeAtlas Tier 2 Postgres image.
#
# Thin re-tag over apache/age. We do not build AGE from source: the
# upstream image already ships the .so for the supported PG major,
# and re-tagging under lithastra/ pins the digest to something we
# control (so a tag-mutation upstream cannot silently change the
# bytes a v1.0 GA install pulls).
#
# Pin matches pkg/store/postgres/testhelper_test.go and
# helm/kubeatlas/values.yaml's persistence.embedded.image default —
# all three move together when we bump AGE / PG.
#
# Multi-arch (linux/amd64 + linux/arm64) build at release time:
#
#   docker buildx build \
#     --platform linux/amd64,linux/arm64 \
#     -t ghcr.io/lithastra/postgres-age:16-1.6.0 \
#     --push \
#     images/postgres-age
#
# CGO INVARIANT (guide §2.2): this image is reachable only as a
# runtime dependency of Tier 2 deployments; nothing in the kubeatlas
# Go binary links against it, so the no-cgo property is unaffected.

FROM apache/age:release_PG16_1.6.0

# AGE needs shared_preload_libraries=age for LOAD 'age' to succeed
# in every session. The upstream image already configures this in
# its entrypoint, but CloudNativePG overrides postgresql.conf via
# the Cluster CRD's postgresql.parameters block, so the chart sets
# shared_preload_libraries there too — see
# helm/kubeatlas/templates/postgres-cluster.yaml.

LABEL org.opencontainers.image.source="https://github.com/lithastra/kubeatlas"
LABEL org.opencontainers.image.licenses="Apache-2.0"
LABEL org.opencontainers.image.description="PostgreSQL 16 + Apache AGE 1.6.0 for KubeAtlas Tier 2"

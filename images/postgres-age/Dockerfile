# syntax=docker/dockerfile:1
#
# lithastra/postgres-age — KubeAtlas Tier 2 Postgres image.
#
# Layered on top of the CloudNativePG operator's Postgres image so
# CNPG's bootstrap tooling (barman, pgaudit) is intact, with Apache
# AGE compiled FROM SOURCE against this image's PG headers + glibc.
#
# Why source build vs COPY of upstream apache/age binaries: the
# upstream image runs Debian Trixie (glibc 2.38), CNPG's image runs
# Bullseye (glibc 2.31), so .so symbols don't resolve at runtime
# ("libc.so.6: version `GLIBC_2.38' not found"). Recompiling against
# 2.31 produces a binary CNPG can actually load.
#
# Build (multi-arch via buildx at release time):
#
#   docker buildx build \
#     --platform linux/amd64,linux/arm64 \
#     -t ghcr.io/lithastra/postgres-age:16.6 \
#     --push \
#     images/postgres-age
#
# Local one-shot for verify/phase2.sh (~5-10 min first time):
#
#   docker build -t local-postgres-age:16.6 images/postgres-age

ARG CNPG_IMAGE=ghcr.io/cloudnative-pg/postgresql:16.6
ARG AGE_VERSION=PG16/v1.6.0-rc0

# ---- stage 1: build AGE from source against CNPG's PG + glibc -----
FROM ${CNPG_IMAGE} AS build

USER root

RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential \
      bison \
      flex \
      git \
      ca-certificates \
      postgresql-server-dev-16 \
    && rm -rf /var/lib/apt/lists/*

ARG AGE_VERSION
RUN git clone --depth 1 --branch ${AGE_VERSION} https://github.com/apache/age.git /tmp/age \
    && cd /tmp/age \
    && make PG_CONFIG=/usr/lib/postgresql/16/bin/pg_config \
    && make install PG_CONFIG=/usr/lib/postgresql/16/bin/pg_config

# ---- stage 2: clean CNPG image + the freshly-compiled AGE -------
FROM ${CNPG_IMAGE}

USER root

# AGE's `make install` writes the .so to PG's pkglibdir and the
# extension SQL/control files to PG's extension dir. Pull only those
# artifacts forward; build-essential and the cloned source stay in
# stage 1.
COPY --from=build /usr/lib/postgresql/16/lib/age.so \
                  /usr/lib/postgresql/16/lib/
COPY --from=build /usr/share/postgresql/16/extension/age.control \
                  /usr/share/postgresql/16/extension/
COPY --from=build /usr/share/postgresql/16/extension/age--*.sql \
                  /usr/share/postgresql/16/extension/

USER postgres

LABEL org.opencontainers.image.source="https://github.com/lithastra/kubeatlas"
LABEL org.opencontainers.image.licenses="Apache-2.0"
LABEL org.opencontainers.image.description="CloudNativePG-compatible Postgres 16 with Apache AGE compiled from source against matching glibc"

# Multi-stage build. The Web UI is built in stage 1 and copied into
# the Go module's embed location so stage 2 can compile it into the
# binary via //go:embed all:web/dist (cmd/kubeatlas/embed.go).
#
# In CI we usually pre-build web/dist at the repo root and let
# goreleaser copy it in; this Dockerfile keeps the npm build inline so
# `docker build .` from a clean checkout works on its own.
#
# The runtime stage is distroless/static + nonroot. That matches the
# Helm Chart's runAsNonRoot=true and readOnlyRootFilesystem=true
# defaults — same security posture, every install path.

# ── Stage 1: Web UI ──────────────────────────────────────────────────
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ── Stage 2: Go binary ───────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Drop the placeholder dist tree and replace with the freshly built
# bundle so //go:embed picks up the real assets.
RUN rm -rf cmd/kubeatlas/web/dist
COPY --from=web /web/dist cmd/kubeatlas/web/dist
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /kubeatlas ./cmd/kubeatlas

# ── Stage 3: Runtime ─────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /kubeatlas /kubeatlas
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/kubeatlas"]

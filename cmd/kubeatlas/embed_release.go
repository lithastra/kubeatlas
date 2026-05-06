//go:build embed_web

package main

import "embed"

// webFS holds the production Web UI bundle. Build with
// `-tags embed_web` after populating cmd/kubeatlas/web/dist (the
// Dockerfile and goreleaser before-hook do this). The all: prefix
// includes dot-prefixed files such as .vite/manifest.json.
//
//go:embed all:web/dist
var webFS embed.FS

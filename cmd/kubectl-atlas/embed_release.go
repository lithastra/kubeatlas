//go:build embed_web

package main

import "embed"

// webFS holds the production Web UI bundle that --local-ui serves
// from the in-process API server. Build with `-tags embed_web` after
// populating cmd/kubectl-atlas/web/dist (the goreleaser before-hook
// copies web/dist there). The all: prefix includes dot-prefixed files
// such as .vite/manifest.json.
//
//go:embed all:web/dist
var webFS embed.FS

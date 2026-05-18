//go:build !embed_web

package main

import "embed"

// webFS is empty in default builds. The //go:embed directive for the
// production Web UI bundle lives behind the embed_web build tag (see
// embed_release.go) so plain `go build`, `go test`, and golangci-lint
// need no populated cmd/kubectl-atlas/web/dist.
//
// --local-ui still runs with an empty webFS: the in-process API server
// degrades the static "/" mount to 404, so only release builds
// (-tags embed_web) actually serve the interactive UI. Offline SVG
// rendering and online mode never touch webFS.
var webFS embed.FS

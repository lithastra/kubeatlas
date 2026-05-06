//go:build !embed_web

package main

import "embed"

// webFS is empty in default builds — `go build`, `go test`, and
// `golangci-lint` all run without populating cmd/kubeatlas/web/dist,
// so the //go:embed directive lives behind the embed_web build tag
// (see embed_release.go). The empty embed.FS still satisfies fs.FS;
// the static handler in pkg/api degrades to 404 when index.html is
// absent.
var webFS embed.FS

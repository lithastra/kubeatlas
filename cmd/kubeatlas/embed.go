package main

import "embed"

// webFS holds the production Web UI bundle that goreleaser copies
// into cmd/kubeatlas/web/dist before building the release binary.
// Local `go build` finds the placeholder .gitkeep and embeds an
// (effectively empty) tree, which is harmless — runWatch only mounts
// the static handler when the bundle has at least an index.html.
//
// The `all:` prefix makes go:embed include dot-prefixed files such as
// .vite/manifest.json that the production build emits.
//
//go:embed all:web/dist
var webFS embed.FS

// Package version exposes build metadata that goreleaser injects via
// -ldflags at release time. Local `go build` keeps the defaults so a
// developer build is always recognisable as such (`Version=="dev"`).
//
// The full ldflag path is:
//
//	-X github.com/lithastra/kubeatlas/pkg/version.Version={{.Version}}
//	-X github.com/lithastra/kubeatlas/pkg/version.Commit={{.Commit}}
//	-X github.com/lithastra/kubeatlas/pkg/version.Date={{.Date}}
//
// If the package path or any variable name changes, .goreleaser.yml
// must change with it — they're a hard contract.
package version

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

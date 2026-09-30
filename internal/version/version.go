// Package version exposes build metadata injected at link time.
package version

import "runtime/debug"

// These are overridden by -ldflags "-X ..." in release builds (see .goreleaser.yaml).
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Name is the canonical server name used in MCP metadata, registries and logs.
const Name = "gemini-media-mcp"

// String returns the version, falling back to the module version recorded by
// `go install module@version` when no ldflags were provided.
func String() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}

// Package version is the single source of truth for Columbo's version
// string. cmd/columbo, cmd/columbo-mcp, and any future binary import
// from here. Closing the version-drift bug class on day zero (see
// Leonard's bughunt-12 F001 and bosun's bughunt-1 F001; we won't ship
// the same finding from this repo).
package version

// Version is overridden at build time via Makefile's ldflags
// (-X github.com/jasondillingham/columbo/internal/version.Version=...).
// The default below is the working-tree-built value when no override
// is set.
var Version = "0.1.0-pre"

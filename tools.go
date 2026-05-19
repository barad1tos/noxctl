//go:build tools

// Package tools anchors build-time-only dependencies so that `go mod tidy`
// keeps them in the direct-require block of go.mod.
//
// Promotes golang.org/x/sync to direct require. The dep-budget test in
// tests/bear/config/no_unexpected_deps_test.go keys off go.mod's
// direct-require block, so the allowlist must list every dep that lands
// here ahead of its first real consumer — otherwise the drift gate fails.
//
// Mechanics: x/sync would otherwise be indirect if no code imports it
// yet. `go get` alone won't promote it without a real source import —
// `go mod tidy` (run via pre-commit) demotes it back. The canonical Go
// fix is a build-tag-gated anchor file. The `//go:build tools` constraint
// excludes this file from `go build`, `go test`, and the linter, but
// `go mod tidy` scans its imports to pin x/sync in the direct-require
// slot. Delete the import line once a real bear/engine consumer lands.
package tools

import (
	_ "golang.org/x/sync/errgroup"
)

//go:build tools

// Package tools anchors build-time-only dependencies so that `go mod tidy`
// keeps them in the direct-require block of go.mod.
//
// (D-07): promote golang.org/x/sync to direct require.
// /2a will import golang.org/x/sync/errgroup in bear/engine for
// per-umbrella parallel apply orchestration (WithContext + ctx-cancel
// propagation). The dep-budget test in
// tests/bear/config/no_unexpected_deps_test.go keys off go.mod's
// direct-require block, so the allowlist must grow ahead of /2a
// landing — otherwise either plan fails its drift gate.
//
// Mechanics: x/sync is currently indirect (none of our code imports it
// yet). `go get` alone won't promote it without a real source import —
// `go mod tidy` (run via pre-commit) demotes it back. The canonical Go
// fix is a build-tag-gated anchor file. The `//go:build tools` constraint
// excludes this file from `go build`, `go test`, and the linter, but
// `go mod tidy` scans its imports to pin x/sync in the direct-require
// slot. File self-removes when /2a lands the real bear/engine
// errgroup import.
//
// Precedent: bcb4c47 (→ 02-04 tools.go for x/sys/unix flock).
package tools

import (
	_ "golang.org/x/sync/errgroup"
)

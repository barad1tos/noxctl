// Package cli holds the noxctl subcommand bodies — destroy, import,
// lint, plan, recap. cmd/noxctl/*.go reduces to cobra wiring + flag
// parsing; the actual orchestration lives here so unit tests under
// tests/bear/cli/<verb>/ can exercise it as an external package
// without spinning up the full CLI surface.
//
// Layering: this package imports bear/domain, bear/render,
// bear/fastpass, bear/engine, bear/config, bear/state but is itself
// imported only by cmd/noxctl. Verbs that grew beyond a single file
// (currently just `verify`) keep their own sub-package.
package cli

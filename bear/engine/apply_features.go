// Package engine is the apply/daemon orchestration layer. It is a
// sibling sub-package of bear/, peer with bear/config/ and bear/state/.
// Layering: engine MAY import bear, bear/state, golang.org/x/sys/unix,
// github.com/fsnotify/fsnotify. engine MUST NOT import bear/config/ —
// the config layer is the CLI's territory.
package engine

// Features toggles each pre-pass at the engine level. cmd/noxctl copies
// values out of *config.Catalog.Features (which uses *bool pointers
// for omitted-vs-false discrimination) into this flat plain-bool
// struct at the CLI boundary; the legacy daemon shim hardcodes all-on.
// Layering invariant: engine/ does NOT import bear/config/.
type Features struct {
	AutoTagDefault    bool
	CrossDomainMoves  bool
	TimePromotion     bool
	ForeignTagEscape  bool
	DuplicateRegistry bool

	// DomainBootstrap gates the universal fast-pass
	// canonicalization pre-pass. When true, the daemon's tick loop runs
	// the new pass that scans every managed `Domain` and lifts atomic
	// notes onto their canonical header (replacing the per-domain copies
	// of the same code path that exist today).
	//
	// Default ON in `AllFeaturesOn`. A reversible kill-switch — flipping
	// `REGEN_DOMAIN_BOOTSTRAP=off` (or `domain_bootstrap = false` in
	// `daemon.toml`) reverts to current per-domain behavior without
	// redeploy.
	DomainBootstrap bool
}

// AllFeaturesOn returns a Features value with every toggle true. Used
// by the legacy daemon shim for production launchd parity.
func AllFeaturesOn() Features {
	return Features{
		AutoTagDefault:    true,
		CrossDomainMoves:  true,
		TimePromotion:     true,
		ForeignTagEscape:  true,
		DuplicateRegistry: true,
		DomainBootstrap:   true,
	}
}

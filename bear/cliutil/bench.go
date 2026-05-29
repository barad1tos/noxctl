package cliutil

import "fmt"

// BenchOpts carries the engine-bound values that the --bench / --concurrency
// flags resolve to. It is the pure-data result of BenchOptsFromFlags; the CLI
// layer copies these fields onto engine.ApplyOpts (WithMetrics +
// BearcliConcurrency). Keeping the mapper a pure function with a plain-struct
// result mirrors the FeaturesFromCatalog boundary-mapper precedent and keeps
// the engine import out of cmd/noxctl.
type BenchOpts struct {
	// WithMetrics maps directly onto engine.ApplyOpts.WithMetrics — true when
	// --bench was passed so the bearcli pool snapshot is copied into
	// ApplyResult.Metrics at completion.
	WithMetrics bool
	// BearcliConcurrency maps onto engine.ApplyOpts.BearcliConcurrency. Zero
	// means "use the engine default" — engine.Apply resolves a non-positive
	// value to DefaultBearcliConcurrency, so the mapper passes 0 through
	// untouched rather than baking the default in at the CLI boundary.
	BearcliConcurrency int
}

// BenchOptsFromFlags maps the --bench bool and --concurrency int flag values
// to the engine-bound BenchOpts. Pure: no I/O, no globals.
//
// Concurrency semantics mirror engine.ApplyOpts.BearcliConcurrency, NOT the
// daemon-toml validateConcurrency rule: a flag value of 0 means "operator did
// not set --concurrency, let the engine apply DefaultBearcliConcurrency", so 0
// passes through. A negative value is operator error and is rejected here so
// the CLI surfaces a clear message instead of letting engine.Apply silently
// re-default it.
func BenchOptsFromFlags(bench bool, concurrency int) (BenchOpts, error) {
	if concurrency < 0 {
		return BenchOpts{}, fmt.Errorf("concurrency = %d: must be >= 0 (0 = engine default)", concurrency)
	}
	return BenchOpts{
		WithMetrics:        bench,
		BearcliConcurrency: concurrency,
	}, nil
}

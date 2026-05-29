package cli

// apply.go implements the noxctl apply subcommand business logic.
//
// cmd/noxctl/apply.go owns Cobra flags and process-level exit-code
// translation; this package owns the test-worthy orchestration:
// pin-registry loading, interrupted-apply warning, engine.Apply
// option assembly, recap rendering, and partial-failure classification.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/barad1tos/noxctl/bear/cliutil"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/state"
)

// ErrApplyInterrupted is returned when engine.Apply reports an
// interrupted run. cmd/noxctl maps it to POSIX exit code 130.
var ErrApplyInterrupted = errors.New("noxctl apply: interrupted")

// ErrApplyFailures is returned when engine.Apply completes but one or
// more pre-pass/domain rows reported Failed > 0.
var ErrApplyFailures = errors.New("noxctl apply: one or more domains failed")

// ApplyOptions carries the resolved apply inputs after Cobra flag
// parsing and catalog preflight.
type ApplyOptions struct {
	Domains   []*domain.Domain
	Catalog   *config.Catalog
	PinTarget string
	StatePath string
	LockPath  string
	NoWait    bool
	Quiet     bool
	Stdout    io.Writer
	Stderr    io.Writer
	// Bench, when set by `noxctl apply --bench`, enables bearcli pool metrics
	// capture for the run (engine.ApplyOpts.WithMetrics). The cycle-telemetry
	// line emits unconditionally either way (Plan 14-03); bench mode additionally
	// retains the snapshot in ApplyResult.Metrics and installs the timing hook.
	Bench bool
	// Concurrency is the operator-supplied --concurrency value (single run) or
	// the per-iteration value the --sweep loop sets in cmd/noxctl. Zero means
	// "engine default" — engine.Apply resolves a non-positive value to
	// DefaultBearcliConcurrency.
	Concurrency int
}

// RunApply runs one noxctl apply pass and renders the recap.
func RunApply(ctx context.Context, opts ApplyOptions) error {
	if opts.StatePath == "" {
		opts.StatePath = "./.noxctl/state.json"
	}
	if opts.LockPath == "" {
		opts.LockPath = "./.noxctl/.lock"
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	pins, _ := domain.LoadPinRegistry(opts.PinTarget)
	warnInterruptedApply(opts.Stderr, opts.StatePath)

	// Map the --bench/--concurrency flags onto the engine-bound fields at the
	// CLI boundary (Pattern A: a parsed-but-unthreaded flag is a silent no-op,
	// guarded end-to-end by tests/bear/cli/apply/bench_wiring_test.go).
	bench, benchErr := cliutil.BenchOptsFromFlags(opts.Bench, opts.Concurrency)
	if benchErr != nil {
		return benchErr
	}

	engineOpts := engine.ApplyOpts{
		Domains:            opts.Domains,
		Pins:               pins,
		StatePath:          opts.StatePath,
		LockPath:           opts.LockPath,
		Features:           cliutil.FeaturesFromCatalog(opts.Catalog),
		NoWait:             opts.NoWait,
		AuditEnabled:       false,
		Stderr:             opts.Stderr,
		DailyDefaultTag:    cliutil.DailyDefaultTagFromCatalog(opts.Catalog),
		PromotionRules:     cliutil.PromotionRulesFromCatalog(opts.Catalog),
		WithMetrics:        bench.WithMetrics,
		BearcliConcurrency: bench.BearcliConcurrency,
	}
	// In bench mode, install a no-op DomainTimingHook so engine.Apply routes
	// per-domain timings through the WithMetrics path; engine.Apply wraps any
	// caller hook in its telemetry accumulator (installTimingAccumulator), so a
	// nil-safe sink is enough to mark this run as bench-instrumented.
	if bench.WithMetrics {
		engineOpts.DomainTimingHook = func(string, time.Duration) {}
	}

	result, runErr := engine.Apply(ctx, engineOpts)
	if result != nil {
		RenderRecap(opts.Stdout, result, opts.Quiet)
	}
	if runErr != nil {
		return runErr
	}
	if result != nil && result.Interrupted {
		return ErrApplyInterrupted
	}
	if result != nil && result.AnyFailed() {
		return ErrApplyFailures
	}
	return nil
}

func warnInterruptedApply(stderr io.Writer, statePath string) {
	st, err := state.Load(statePath)
	if err == nil && st.InProgress.Verb == "apply" {
		_, _ = fmt.Fprintf(stderr, "noxctl: resuming after interrupted apply (started %s)\n",
			st.InProgress.StartedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}
}

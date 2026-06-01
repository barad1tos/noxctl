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

	"github.com/barad1tos/noxctl/bear/bearcli"
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
	// capture for the run (engine.ApplyOpts.WithMetrics) and renders a concise
	// stdout summary. Per-domain timings are accumulated by engine.Apply on
	// every run (not just bench), so no caller hook is needed.
	Bench bool
	// Concurrency is the operator-supplied --concurrency value (single run) or
	// the per-iteration value the --sweep loop sets in cmd/noxctl. Zero means
	// "engine default" — engine.Apply resolves a non-positive value to
	// DefaultBearcliConcurrency.
	Concurrency int
}

// RunApplySweep runs one apply per concurrency value in sweep, re-arming the
// global bearcli pool to each new capacity between iterations so engine.Apply's
// SetConcurrency takes effect for that iteration (the pool's SetConcurrency is
// sync.Once-gated, so without the re-arm only the first value would stick). An
// empty sweep is the single-run path and runs exactly once at optsFor's own
// concurrency. optsFor builds the ApplyOptions for the given per-iteration
// concurrency; the caller owns flag plumbing and stdout banners.
//
// bearcli.ResetPoolForTest is the pool's sanctioned re-arm seam — its doc
// reserves it for "the bench --sweep mode that measures throughput across
// multiple concurrency values in one run". The daemon hot path must never use
// it.
func RunApplySweep(ctx context.Context, sweep []int, optsFor func(concurrency int) ApplyOptions) error {
	if len(sweep) == 0 {
		return RunApply(ctx, optsFor(0))
	}
	for i, n := range sweep {
		if i > 0 {
			bearcli.ResetPoolForTest(n)
		}
		if err := RunApply(ctx, optsFor(n)); err != nil {
			return err
		}
	}
	return nil
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

	pins, pinErr := domain.LoadPinRegistry(opts.PinTarget)
	if pinErr != nil {
		_, _ = fmt.Fprintf(opts.Stderr,
			"warning: pin registry %q failed to load: %v (proceeding with no pins)\n",
			opts.PinTarget, pinErr)
	}
	warnInterruptedApply(opts.Stderr, opts.StatePath)

	// Map the --bench/--concurrency flags onto the engine-bound fields at the
	// CLI boundary (a parsed-but-unthreaded flag would be a silent no-op,
	// guarded end-to-end by tests/bear/cli/apply/bench_wiring_test.go).
	bench, benchErr := cliutil.BenchOptsFromFlags(opts.Bench, opts.Concurrency)
	if benchErr != nil {
		return benchErr
	}
	engineOpts := engine.ApplyOpts{
		Domains:                opts.Domains,
		Pins:                   pins,
		StatePath:              opts.StatePath,
		LockPath:               opts.LockPath,
		Features:               cliutil.FeaturesFromCatalog(opts.Catalog),
		NoWait:                 opts.NoWait,
		AuditEnabled:           false,
		Stderr:                 opts.Stderr,
		DailyDefaultTag:        cliutil.DailyDefaultTagFromCatalog(opts.Catalog),
		PromotionRules:         cliutil.PromotionRulesFromCatalog(opts.Catalog),
		WithMetrics:            bench.WithMetrics,
		BearcliConcurrency:     bench.BearcliConcurrency,
		SuppressCycleTelemetry: opts.Quiet,
	}

	result, runErr := engine.Apply(ctx, engineOpts)
	if result != nil {
		RenderRecap(opts.Stdout, result, opts.Quiet)
	}
	if runErr != nil {
		return runErr
	}
	if result != nil && opts.Bench {
		RenderBenchSummary(opts.Stdout, result)
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

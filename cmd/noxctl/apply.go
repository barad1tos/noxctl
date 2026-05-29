package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/cliutil"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
)

// errInterrupted is the sentinel that cmd/noxctl/main.go maps to
// POSIX exit code 130 (128 + SIGINT).
var errInterrupted = cliutil.ErrInterrupted

// errApplyFailures is returned when apply completed without a
// top-level error but at least one pre-pass or per-domain row had
// Failed > 0. Maps to exit 1 in main.go.
var errApplyFailures = errors.New("noxctl: one or more domains failed")

// CLI-state for apply-specific flags. Declared at package scope so
// `resolveBearDB` and other cmd-layer helpers don't have to thread
// them through every catalog-load callsite.
var (
	applyNoWait      bool   // --no-wait
	applyAutoApprove bool   // --auto-approve (reserved no-op v1)
	applyBearDBFlag  string // --bear-db override (reserved for completeness; one-shot apply doesn't watch)
	applyBench       bool   // --bench (enable bearcli pool metrics for the run)
	applySweep       string // --sweep (comma-separated concurrency values, e.g. "4,8")
	applyConcurrency int    // --concurrency (single-run bearcli pool cap; 0 = engine default)
)

// applyCmd is the real `noxctl apply` subcommand. Loads noxctl.toml,
// delegates apply orchestration to bear/cli, and maps SIGINT/SIGTERM
// cancellation to errInterrupted (exit 130).
var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply noxctl.toml to Bear (one-shot regen)",
	Long: `Apply runs the full regen cycle once — equivalent to regen-watchd --once.

Loads noxctl.toml, runs pre-passes (foreign-tag escape, auto-tag,
cross-domain moves, time-promotion, duplicate registry — toggleable via
[features]), then iterates domains through bear/regen. Persists
per-domain progress to ./.noxctl/state.json incrementally so partial-
success state is recoverable.

Concurrency: serializes through ./.noxctl/.lock via flock. By default
blocks forever waiting for the lock; --no-wait fails fast if held.
SIGINT mid-apply persists state.json with InProgress marker and exits
130; rerunning resumes idempotently.

Exit codes: 0=success, 1=error or per-domain failures, 130=interrupted by SIGINT.`,
	RunE: runApply,
}

// runApply is the apply RunE. Extracted to a named function so the
// command literal stays small and so the gocognit budget is enforced
// against a single named symbol rather than an anonymous closure.
func runApply(cmd *cobra.Command, _ []string) error {
	domains, cat, target, loadErr := domainsWithPreflight()
	if loadErr != nil {
		return loadErr
	}
	sweep, sweepErr := cliutil.ParseSweep(applySweep)
	if sweepErr != nil {
		return sweepErr
	}
	// Fail fast: --concurrency is per-iteration-overridden by --sweep, so passing
	// both means the operator's --concurrency would be silently ignored. Reject
	// the combination at the boundary instead of dropping the value.
	if len(sweep) > 0 && applyConcurrency != 0 {
		return fmt.Errorf("--concurrency cannot be combined with --sweep "+
			"(--sweep drives concurrency per iteration; got --concurrency=%d)", applyConcurrency)
	}

	runErr := runWithSignalContext(cmd, func(ctx context.Context) error {
		return cli.RunApplySweep(ctx, sweep, func(concurrency int) cli.ApplyOptions {
			if len(sweep) > 0 {
				_, _ = fmt.Fprintf(os.Stdout, "noxctl apply --bench: concurrency=%d\n", concurrency)
				return applyOptionsFor(domains, cat, target, concurrency)
			}
			return applyOptionsFor(domains, cat, target, applyConcurrency)
		})
	})
	switch {
	case errors.Is(runErr, cli.ErrApplyInterrupted):
		return errInterrupted
	case errors.Is(runErr, cli.ErrApplyFailures):
		return errApplyFailures
	}
	return runErr
}

// applyOptionsFor builds the cli.ApplyOptions for one run at the given
// concurrency. --sweep implies --bench so each swept run captures metrics.
func applyOptionsFor(domains []*domain.Domain, cat *config.Catalog, target string, concurrency int) cli.ApplyOptions {
	return cli.ApplyOptions{
		Domains:     domains,
		Catalog:     cat,
		PinTarget:   target,
		NoWait:      applyNoWait,
		Quiet:       quiet,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Bench:       applyBench || applySweep != "",
		Concurrency: concurrency,
	}
}

func init() {
	applyCmd.Flags().BoolVar(&applyNoWait, "no-wait", false,
		"fail fast if ./.noxctl/.lock is held by another process (default: block forever)")
	applyCmd.Flags().BoolVar(&applyAutoApprove, "auto-approve", false,
		"reserved for future destructive verbs; accepted as a no-op in v1")
	applyCmd.Flags().StringVar(&applyBearDBFlag, "bear-db", "",
		"Bear DB watch directory (precedence: this flag > BEAR_DB_DIR env > [meta].bear_db > default)")
	applyCmd.Flags().BoolVar(&applyBench, "bench", false,
		"enable bearcli pool metrics capture for this apply run (telemetry line prints either way)")
	applyCmd.Flags().StringVar(&applySweep, "sweep", "",
		"comma-separated concurrency values to benchmark (e.g. \"4,8\"); implies --bench, re-arms the pool per value")
	applyCmd.Flags().IntVar(&applyConcurrency, "concurrency", 0,
		"bearcli subprocess concurrency cap for this run (0 = engine default; cannot be combined with --sweep)")
	rootCmd.AddCommand(applyCmd)
}

// --auto-approve is declared as a forward-compatibility no-op for
// future destructive verbs; it has no consumer in v1. The blank
// assignment silences the `unused` lint.
var _ = applyAutoApprove

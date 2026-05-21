package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/state"
)

// errInterrupted is the sentinel that cmd/noxctl/main.go maps to
// POSIX exit code 130 (128 + SIGINT).
var errInterrupted = errors.New("noxctl: interrupted")

// errApplyFailures is returned when engine.Apply completed without a
// top-level error but at least one pre-pass or per-domain row had
// `Failed > 0`. Maps to exit 1 in main.go.
var errApplyFailures = errors.New("noxctl: one or more domains failed")

// CLI-state for apply-specific flags. Declared at package scope so
// `featuresFromCatalog` / `resolveBearDB` don't have to thread them.
var (
	applyNoWait      bool   // --no-wait
	applyAutoApprove bool   // --auto-approve (reserved no-op v1)
	applyBearDBFlag  string // --bear-db override (reserved for completeness; one-shot apply doesn't watch)
)

// applyCmd is the real `noxctl apply` subcommand. Replaces the
// stub. Loads noxctl.toml, runs `engine.Apply`, renders the PLAY RECAP
// via `renderRecap` (cmd/noxctl/recap.go, text/tabwriter), and maps
// SIGINT/SIGTERM cancellation to errInterrupted (exit 130).
var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply noxctl.toml to Bear (one-shot regen)",
	Long: `Apply runs the full regen cycle once — equivalent to regen-watchd --once.

Loads noxctl.toml, runs pre-passes (foreign-tag escape, auto-tag,
cross-domain moves, time-promotion, duplicate registry — toggleable via
[features]), then iterates domains calling RunRegen for each. Persists
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
	// Inline preflight — mirrors validate.go:50-56.
	legacyPath, target := pinPaths()
	if migrationErr := state.MigratePins(legacyPath, target); migrationErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: pin migration failed: %v\n", migrationErr)
	}

	// Config load — uniform error shape via formattedLoadError.
	domains, cat, loadErr := config.Load(configPath)
	if loadErr != nil {
		return &formattedLoadError{
			inner: loadErr,
			msg:   config.FormatLoadError(loadErr, configPath),
		}
	}

	// Pin registry — best-effort load (nil-safe registry per bear/pins.go).
	pins, _ := domain.LoadPinRegistry(target)

	// Resume detection — single stderr warning, no prompt.
	if st, stErr := state.Load("./.noxctl/state.json"); stErr == nil && st.InProgress.Verb == "apply" {
		_, _ = fmt.Fprintf(os.Stderr, "noxctl: resuming after interrupted apply (started %s)\n",
			st.InProgress.StartedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}

	// SIGINT/SIGTERM bridging: signal.NotifyContext cancels ctx,
	// engine.Apply persists InProgress state, main maps to exit 130.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := engine.ApplyOpts{
		Domains:         domains,
		Pins:            pins,
		StatePath:       "./.noxctl/state.json",
		LockPath:        "./.noxctl/.lock",
		Features:        featuresFromCatalog(cat),
		NoWait:          applyNoWait,
		AuditEnabled:    false,
		Stderr:          os.Stderr,
		DailyDefaultTag: dailyDefaultTagFromCatalog(cat),
		PromotionRules:  promotionRulesFromCatalog(cat),
	}

	result, runErr := engine.Apply(ctx, opts)
	if result != nil {
		cli.RenderRecap(os.Stdout, result, quiet)
	}
	if runErr != nil {
		return runErr
	}
	if result != nil && result.Interrupted {
		return errInterrupted
	}
	if result != nil && result.AnyFailed() {
		return errApplyFailures
	}
	return nil
}

func init() {
	applyCmd.Flags().BoolVar(&applyNoWait, "no-wait", false,
		"fail fast if ./.noxctl/.lock is held by another process (default: block forever)")
	applyCmd.Flags().BoolVar(&applyAutoApprove, "auto-approve", false,
		"reserved for future destructive verbs; accepted as a no-op in v1")
	applyCmd.Flags().StringVar(&applyBearDBFlag, "bear-db", "",
		"Bear DB watch directory (precedence: this flag > BEAR_DB_DIR env > [meta].bear_db > default)")
	rootCmd.AddCommand(applyCmd)
}

// --auto-approve is declared as a forward-compatibility no-op for
// future destructive verbs; it has no consumer in v1. The blank
// assignment silences the `unused` lint.
var _ = applyAutoApprove

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
)

var daemonBearDBFlag string // --bear-db override

// errFmtNoxctlDaemon prefixes every error returned by `noxctl daemon`
// so cobra's RunE handler renders a consistent `noxctl daemon: ...`
// prefix to stderr. Extracted to a const so the literal is defined
// once instead of repeated at every return site.
const errFmtNoxctlDaemon = "noxctl daemon: %w"

// daemonCmd is the real `noxctl daemon` subcommand. Replaces the
// stub. Loads noxctl.toml, constructs `engine.NewDaemon`, and runs the
// FSEvents-driven watcher until SIGINT/SIGTERM triggers graceful
// shutdown (exit 0; SIGINT-as-error is apply-only).
var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the FSEvents-driven background watcher",
	Long: `Daemon runs the long-running watcher that triggers a regen cycle whenever
Bear's SQLite database changes. Uses fsnotify's kqueue backend on macOS.

Per-cycle flock at ./.noxctl/.lock serializes with manual ` + "`noxctl apply`" + `
invocations; if a noxctl apply touches ./.noxctl/.apply-pending the daemon
yields its current cycle and lets apply proceed. Self-write epsilon (2s)
prevents the daemon from looping on its own bearcli writes.

Graceful shutdown on SIGINT/SIGTERM: drains the in-flight regen cycle,
releases the flock, exits 0.

Exit codes: 0=graceful shutdown or clean exit, 1=startup or runtime error.`,
	RunE: runDaemon,
}

// runDaemon is the daemon RunE. Extracted to a named function so the
// command literal stays small (mirrors apply.go::runApply).
func runDaemon(cmd *cobra.Command, _ []string) error {
	// Microsecond-precision timestamps match the format the legacy
	// daemon binary emitted, so log diff tooling and operator's eye
	// keep working across the rename.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	domains, cat, target, loadErr := domainsWithPreflight()
	if loadErr != nil {
		return loadErr
	}

	// Load daemon-toml runtime config (poll intervals, debounce, audit
	// gate, bearcli concurrency). LoadDaemon tolerates a missing file
	// (returns defaults) so an operator with only a catalog still gets
	// a working daemon.
	dc, daemonErr := config.LoadDaemon(daemonConfigPath())
	if daemonErr != nil {
		return fmt.Errorf(errFmtNoxctlDaemon, daemonErr)
	}

	pins, _ := domain.LoadPinRegistry(target)

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bearDBDir, bearDBErr := resolveBearDB(cat, daemonBearDBFlag)
	if bearDBErr != nil {
		return fmt.Errorf(errFmtNoxctlDaemon, bearDBErr)
	}
	opts := engine.DaemonOpts{
		ApplyOpts: engine.ApplyOpts{
			Domains:            domains,
			Pins:               pins,
			StatePath:          dc.StatePath,
			LockPath:           dc.LockPath,
			Features:           featuresFromCatalog(cat),
			AuditEnabled:       dc.AuditEnabled,
			BearcliConcurrency: dc.BearcliConcurrency,
			Stderr:             os.Stderr,
			DailyDefaultTag:    dailyDefaultTagFromCatalog(cat),
			PromotionRules:     promotionRulesFromCatalog(cat),
		},
		BearDBDir:           bearDBDir,
		DebouncePause:       dc.DebouncePause,
		MaxBurstWindow:      dc.MaxBurstWindow,
		MtimePollInterval:   dc.MtimePollInterval,
		AutoTagPollInterval: dc.AutoTagPollInterval,
	}

	// Surface silently-no-op'd fast-passes once at startup so the
	// operator notices catalog/runtime drift without grepping every
	// poll tick. The two passes have different gates: foreign-tag
	// escape is gated only on `Features.ForeignTagEscape`; daily
	// default stamping requires both the feature flag AND a non-empty
	// [meta].daily_default_tag in the catalog, and silently no-ops
	// when the catalog omits the latter. Emit a one-shot WARN so the
	// next operator who sees "untagged note didn't roll" finds a
	// breadcrumb in the log.
	if opts.Features.AutoTagDefault && opts.DailyDefaultTag == "" {
		log.Printf("WARN: daily-default tag stamping inactive — features.auto_tag_default=true " +
			"but [meta].daily_default_tag is unset in the catalog; untagged notes will " +
			"NOT be stamped with a default tag until the catalog declares one " +
			"(example: [meta].daily_default_tag = \"quicknote/daily\")")
	}

	// Emit the startup marker `noxctl verify --check daemon-log` rewinds
	// to. Sourced from engine.DaemonStartupLogMarker so this emit and
	// the verify-side scanner share one source of truth — rename the
	// marker in bear/engine/daemon.go and both sides follow.
	log.Printf("%s; watching dir %s, domains=%d",
		engine.DaemonStartupLogMarker, opts.BearDBDir, len(domains))

	d, err := engine.NewDaemon(opts)
	if err != nil {
		return fmt.Errorf(errFmtNoxctlDaemon, err)
	}
	defer func() {
		if closeErr := d.Close(); closeErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "noxctl daemon close: %v\n", closeErr)
		}
	}()

	// Daemon SIGINT is graceful shutdown, NOT an error — the
	// SIGINT-as-error policy applies only to `apply`. Run returns
	// ctx.Err on cancel; squash to nil for exit 0.
	if runErr := d.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf(errFmtNoxctlDaemon, runErr)
	}
	return nil
}

func init() {
	daemonCmd.Flags().StringVar(&daemonBearDBFlag, "bear-db", "",
		"Bear DB watch directory (precedence: this flag > BEAR_DB_DIR env > [meta].bear_db > default)")
	rootCmd.AddCommand(daemonCmd)
}

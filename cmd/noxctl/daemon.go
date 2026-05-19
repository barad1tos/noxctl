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

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/state"
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

	// Inline preflight — mirrors apply.go.
	legacyPath, target := pinPaths()
	if migrationErr := state.MigratePins(legacyPath, target); migrationErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: pin migration failed: %v\n", migrationErr)
	}

	domains, cat, loadErr := config.Load(cfgPath)
	if loadErr != nil {
		return &formattedLoadError{
			inner: loadErr,
			msg:   config.FormatLoadError(loadErr, cfgPath),
		}
	}

	pins, _ := bear.LoadPinRegistry(target)

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bearDBDir, bearDBErr := resolveBearDB(cat, daemonBearDBFlag)
	if bearDBErr != nil {
		return fmt.Errorf(errFmtNoxctlDaemon, bearDBErr)
	}
	opts := engine.DaemonOpts{
		ApplyOpts: engine.ApplyOpts{
			Domains:         domains,
			Pins:            pins,
			StatePath:       "./.noxctl/state.json",
			LockPath:        "./.noxctl/.lock",
			Features:        featuresFromCatalog(cat),
			AuditEnabled:    false,
			Stderr:          os.Stderr,
			DailyDefaultTag: dailyDefaultTagFromCatalog(cat),
			PromotionRules:  promotionRulesFromCatalog(cat),
		},
		BearDBDir: bearDBDir,
	}

	// Emit the startup marker `noxctl verify --check daemon-log` rewinds
	// to. Wording preserved from the legacy daemon (`regen-watchd
	// starting`) so the verify-gate scanner keeps matching post-rebrand
	// without touching the constant in bear/cli/verify/checks.go.
	log.Printf("regen-watchd starting; watching dir %s, domains=%d",
		opts.BearDBDir, len(domains))

	d, err := engine.NewDaemon(opts)
	if err != nil {
		return fmt.Errorf(errFmtNoxctlDaemon, err)
	}
	defer func() {
		if cerr := d.Close(); cerr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "noxctl daemon close: %v\n", cerr)
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

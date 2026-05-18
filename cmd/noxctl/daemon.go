package main

import (
	"context"
	"errors"
	"fmt"
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

// daemonCmd is the real `noxctl daemon` subcommand. Replaces the
// stub. Loads noxctl.toml, constructs `engine.NewDaemon`, and runs the
// FSEvents-driven watcher until SIGINT/SIGTERM triggers graceful
// shutdown (exit 0 per CLI-08 narrowed-to-apply contract).
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

	opts := engine.DaemonOpts{
		ApplyOpts: engine.ApplyOpts{
			Domains:      domains,
			Pins:         pins,
			StatePath:    "./.noxctl/state.json",
			LockPath:     "./.noxctl/.lock",
			Features:     featuresFromCatalog(cat),
			AuditEnabled: false,
			Stderr:       os.Stderr,
		},
		BearDBDir: resolveBearDB(cat, daemonBearDBFlag),
	}

	d, err := engine.NewDaemon(opts)
	if err != nil {
		return fmt.Errorf("noxctl daemon: %w", err)
	}
	defer func() {
		if cerr := d.Close(); cerr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "noxctl daemon close: %v\n", cerr)
		}
	}()

	// Daemon SIGINT is graceful shutdown, NOT an error per CLI-08
	// narrowed to apply-only. Run returns ctx.Err on cancel; we
	// squash that to nil for exit 0.
	if runErr := d.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("noxctl daemon: %w", runErr)
	}
	return nil
}

func init() {
	daemonCmd.Flags().StringVar(&daemonBearDBFlag, "bear-db", "",
		"Bear DB watch directory (precedence: this flag > BEAR_DB_DIR env > [meta].bear_db > default)")
	rootCmd.AddCommand(daemonCmd)
}

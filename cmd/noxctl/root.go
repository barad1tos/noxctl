package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Build-time injectables — wired via -ldflags=-X main.version=…
// kept as package-level vars (not consts) so a release build can
// stamp them without touching source.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// Persistent flag bindings — populated by Cobra during flag parsing,
// consumed by PersistentPreRunE and subcommand bodies.
// defaultConfigPath is the single source of truth for the
// noxctl.toml location every subcommand falls back to when --config
// is not supplied. Lives in root.go so the cobra `--config` flag
// default + the `noxctl init` positional-arg fallback agree by
// construction.
const defaultConfigPath = "./noxctl.toml"

var (
	configPath string
	verbose    bool
	quiet      bool
)

// rootCmd is the noxctl entry. Subcommand wiring happens in each
// subcommand's init via rootCmd.AddCommand so main.go stays small
// and each verb owns its own flag surface.
var rootCmd = &cobra.Command{
	Use:   "noxctl",
	Short: "Declarative Bear-notes structure manager",
	Long: "noxctl applies a per-project noxctl.toml description of Bear " +
		"tags/hubs/masters/buckets idempotently — Terraform for Bear notes.",
	// Don't print usage on errors — too noisy. Cobra still prints
	// the error itself which is what we want on stderr.
	SilenceUsage:  true,
	SilenceErrors: false,
	Version:       version,
}

func init() {
	rootCmd.SetVersionTemplate(fmt.Sprintf(
		"noxctl {{.Version}} (commit %s, built %s)\n", commit, date,
	))
	rootCmd.PersistentFlags().StringVar(&configPath, "config", defaultConfigPath,
		"path to noxctl.toml (no walk-up; explicit only)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false,
		"verbose stderr output")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false,
		"suppress success messages on stderr")
	// quiet/verbose are mutually exclusive. Declared here (root.go
	// init) so the flag-group registration happens AFTER the
	// persistent flags are bound, regardless of subcommand init
	// order.
	rootCmd.MarkFlagsMutuallyExclusive("quiet", "verbose")
}

// Exit-code constants follow the Terraform-style detailed-exitcode
// contract: 0 success, 1 error, 2 diff present (plan), 130 SIGINT
// mid-apply (POSIX 128 + signal number).
const (
	ExitSuccess     = 0
	ExitError       = 1
	ExitDiffPresent = 2
	ExitInterrupted = 130 // POSIX 128 + SIGINT
)

// exitWith centralizes os.Exit so tests can substitute a recorder.
// Production code uses os.Exit; main calls it indirectly via the
// returned error from rootCmd.Execute. The blank-assignment below
// keeps the variable referenced until / wires it
// from the apply / plan diff-exit paths.
var (
	exitWith = os.Exit
	_        = exitWith
)

// Keeper for the Terraform-style exit-code quartet. declared
// the original trio (ExitSuccess/ExitError/ExitDiffPresent);
// added ExitInterrupted as the first SIGINT-aware reader.
// 's `noxctl plan` is the first reader of ExitDiffPresent.
var _ = [4]int{ExitSuccess, ExitError, ExitDiffPresent, ExitInterrupted}

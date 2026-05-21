package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/state"
)

// validateCmd is the only REAL subcommand in. Loads the
// noxctl.toml at the resolved path, runs the full strict-mode
// loader (decode → Undecoded → ValidateCatalog → Dispatch →
// Domain.Validate, all aggregated through errors.Join), prints a
// verbose success summary on stderr, and exits 0/1.
//
// validate has no PersistentPreRunE because validate IS the
// preflight — running a separate PersistentPreRunE step would
// duplicate MigratePins logging and the aggregate-error path on the
// same load. Stubs deliberately skip preflight entirely (see
// stub.go); validate inlines pin migration so the user-facing
// surface is identical to a future real subcommand without the
// double-load.
//
// validate never spawns bearcli; the static guarantee at
// tests/bear/config/loader_test.go::TestLoaderZeroBearcli plus the
// e2e smoke test verify both compile-time and runtime that no
// Bear-side I/O happens here.
var validateCmd = &cobra.Command{
	Use:   "validate [path]",
	Short: "Validate noxctl.toml without any Bear-side I/O",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		start := time.Now()

		// Path resolution: positional arg overrides --config flag,
		// which defaults to "./noxctl.toml". No env-var override,
		// no walk-up — the path is always explicit.
		path := configPath
		if len(args) == 1 {
			path = args[0]
		}

		// Inline preflight — see validateCmd doc comment for why
		// we don't reuse preflightAndLoad here.
		legacyPath, target := pinPaths()
		if migrationErr := state.MigratePins(legacyPath, target); migrationErr != nil {
			// Stderr write errors are unrecoverable here (broken pipe / closed
			// fd) and not actionable for the user; explicit blank-assignment
			// silences errcheck while keeping intent visible.
			_, _ = fmt.Fprintf(os.Stderr, "warning: pin migration failed: %v\n", migrationErr)
		}

		domains, cat, loadErr := config.Load(path)
		if loadErr != nil {
			// Cobra surfaces this to stderr + sets exit 1.
			// Wrap so stderr shows the uniform `path:line:col: kind:
			// message` shape while preserving errors.Is reachability
			// of the original chain (e.g. fs.ErrNotExist).
			return &formattedLoadError{
				inner: loadErr,
				msg:   config.FormatLoadError(loadErr, path),
			}
		}

		if !quiet {
			// Same rationale as the migration warning above — stderr write
			// errors are unrecoverable and not user-actionable here.
			_, _ = fmt.Fprintf(os.Stderr,
				"✓ %d domains validated, %d blueprints used, 0 issues (%dms)\n",
				len(domains), uniqueBlueprints(cat), time.Since(start).Milliseconds())
		}
		return nil
	},
}

func init() { rootCmd.AddCommand(validateCmd) }

// formattedLoadError preserves errors.Is reachability of the underlying
// error (so existing tests like TestLoaderFileNotFound continue to
// satisfy errors.Is(returned, fs.ErrNotExist)) while overriding
// Error so cobra's stderr output shows the uniform shape produced
// by config.FormatLoadError.
type formattedLoadError struct {
	inner error
	msg   string
}

func (e *formattedLoadError) Error() string { return e.msg }
func (e *formattedLoadError) Unwrap() error { return e.inner }

// uniqueBlueprints counts distinct Blueprint values in the catalog.
// nil-safe so callers don't have to guard.
func uniqueBlueprints(cat *config.Catalog) int {
	if cat == nil {
		return 0
	}
	seen := map[string]struct{}{}
	for _, d := range cat.Domains {
		seen[d.Blueprint] = struct{}{}
	}
	return len(seen)
}

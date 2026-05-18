// noxctl — declarative CLI for Bear-notes structure management.
//
// shipped only `noxctl validate` as a real subcommand;
// (Plans 02-06+) wires `apply` and `daemon`. The remaining four
// (init, plan, destroy, import) stay stubbed until later phases fill
// in their bodies (: plan,: init/destroy/import).
package main

import (
	"errors"
	"os"

	"github.com/barad1tos/noxctl/bear/cli/parity"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		// Cobra has already printed the error to stderr; do not
		// double-print. Map errInterrupted (SIGINT mid-apply) to
		// POSIX exit 130; everything else is a generic exit 1.
		if errors.Is(err, errInterrupted) {
			os.Exit(ExitInterrupted) // 130 = POSIX 128 + SIGINT
		}
		if errors.Is(err, errDriftDetected) {
			os.Exit(ExitDiffPresent) // 2 — Terraform -detailed-exitcode
		}
		// parity-check: D-15 overrides CLI-04 for this
		// subcommand only — exit 2 means "cache state malformed", not
		// "drift exists". parity.ErrFailed maps to the generic exit 1.
		if errors.Is(err, parity.ErrCacheError) {
			os.Exit(ExitDiffPresent) // 2 — overloaded as parity-check ERROR
		}
		if errors.Is(err, parity.ErrFailed) {
			os.Exit(ExitError) // 1
		}
		os.Exit(ExitError) // 1
	}
}

// noxctl — declarative CLI for Bear-notes structure management.
package main

import (
	"errors"
	"os"

	"github.com/barad1tos/noxctl/bear/cli"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		// Cobra has already printed the error to stderr; do not
		// double-print. Map errInterrupted (SIGINT mid-apply) to
		// POSIX exit 130; everything else is a generic exit 1.
		if errors.Is(err, errInterrupted) {
			os.Exit(ExitInterrupted) // 130 = POSIX 128 + SIGINT
		}
		if errors.Is(err, errDestroyAborted) {
			// runDestroy already wrote "noxctl destroy: aborted" to
			// stderr; suppress Cobra's redundant "Error: ..." by
			// silencing here. Exit 1 — operator declined.
			os.Exit(ExitError)
		}
		if errors.Is(err, errDriftDetected) {
			os.Exit(ExitDiffPresent) // 2 — Terraform -detailed-exitcode
		}
		// Lint reported per-atom failures the operator must address.
		// Mirrors the drift exit-code contract: exit-2 means "the gate
		// answered NO," not "the gate could not run."
		if errors.Is(err, cli.ErrLintFailed) {
			os.Exit(ExitDiffPresent) // 2
		}
		// `noxctl verify` reuses the Terraform exit-2 contract for
		// "gate said no" (drift / warnings / non-idempotent) and the
		// generic exit-1 for "gate could not ask the question"
		// (bearcli unreachable, daemon log absent, etc.). Without
		// these arms both fall through to exit 1 and consumers
		// (CI, scripts/ship-gate.sh) cannot tell FAIL apart from a
		// panic.
		if errors.Is(err, errVerifyFailed) {
			os.Exit(ExitDiffPresent) // 2
		}
		if errors.Is(err, errVerifyRuntime) {
			os.Exit(ExitError) // 1 — explicit; matches default fallthrough but documents intent
		}
		os.Exit(ExitError) // 1
	}
}

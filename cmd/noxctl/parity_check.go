// parity_check.go — cobra wiring for the parity-check subcommand. The
// D-10/D-11 implementation lives in bear/cli/parity (parity.Run); this
// file only binds the CLI flags and dispatches into the library.
package main

import (
	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/cli/parity"
)

// CLI-state for parity-check-specific flags. Declared at package scope
// so the cobra Flags binding survives across `cmd.Execute` rounds.
var (
	parityCheckDays     int
	parityCheckCacheDir string
)

// parityCheckCmd is the parity-check subcommand. Read-only consumer of
// the daily JSON files written by the parity launchd cron — never
// mutates Bear, never invokes engine.Plan.
var parityCheckCmd = &cobra.Command{
	Use:   "parity-check",
	Short: "Verify the  deletion gate from daily parity logs",
	Long: `parity-check reads the most recent --days entries from
--cache-dir (default ~/.cache/noxctl-parity), each a JSON-serialized
engine.PlanResult written by the daily launchd cron, and reports
PASS / FAIL based on consecutive clean days.

A day is "clean" iff its PlanResult summary has 0 parity-mismatched
domains AND 0 errored domains. ANY drift resets the streak to 0
(D-11 strict reset). DomainsDrift (single-path drift) and
UntrackedFamilies (residue) do NOT contribute — parity-check only
asks "did the TOML and the hardcoded path agree?".

Exit codes:
  0 — PASS (--days consecutive clean days achieved)
  1 — FAIL (drift in window; streak < --days)
  2 — ERROR (cache directory missing or unreadable)

Note: the exit-2 semantics here override CLI-04's "diff exists"
convention. parity-check doesn't emit drift in the Terraform sense;
2 means "cache state is malformed, please investigate".`,
	RunE: runParityCheck,
}

func init() {
	parityCheckCmd.Flags().IntVar(&parityCheckDays, "days", 7,
		"required consecutive clean days")
	parityCheckCmd.Flags().StringVar(&parityCheckCacheDir, "cache-dir",
		parity.DefaultCacheDir(),
		"directory holding daily parity JSON files (default ~/.cache/noxctl-parity)")
	rootCmd.AddCommand(parityCheckCmd)
}

// runParityCheck is the parity-check RunE. Thin shim — defers all
// listing/parse/streak logic to parity.Run.
func runParityCheck(cmd *cobra.Command, _ []string) error {
	return parity.Run(cmd.OutOrStdout(), parityCheckCacheDir, parityCheckDays)
}

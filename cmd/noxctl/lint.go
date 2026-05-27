package main

import (
	"github.com/spf13/cobra"
)

var lintApply bool // --apply flag

// lintCmd runs the auto-fix orchestrator over every managed domain.
// Without --apply behaves identically to 'noxctl audit' (report-only).
// With --apply walks every domain, lists its atoms, and rewrites those
// flagged Fixable. Idempotent — a second run on a clean corpus is a
// no-op.
//
// Exit codes:
//
//   - 0 — sweep completed
//   - 1 — load error or partial sweep failure
//   - 130 — interrupted via SIGINT/SIGTERM
var lintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Report lint findings; with --apply auto-fix the fixable ones",
	Long: `Lint scans every managed-tag atom for fixable structural
defects. Report-only by default — without --apply lint just prints
the same grouped findings 'noxctl audit' shows, so a stray
invocation cannot modify the vault. Pass --apply to rewrite the
Fixable rows in place.

With --apply, the fixable findings (broken-H1 titles, malformed
canonical tag-lines) are rewritten through bearcli. Duplicate-title
findings are triaged with #orphans/duplicate-title; note renaming
remains manual. Non-fixable findings (unsafe-title,
missing-canonical that requires user input) are logged for manual
review. Re-running on a clean corpus produces no writes.`,
	RunE: runLint,
}

// runLint is the lint RunE. Body delegates to runLintSweep (shared
// with audit). The --apply flag picks the path: report-only when
// false (muscle-memory match with `noxctl audit`); auto-fix
// orchestrator when true.
func runLint(cmd *cobra.Command, _ []string) error {
	return runLintSweep(cmd, lintApply)
}

func init() {
	lintCmd.Flags().BoolVar(&lintApply, "apply", false,
		"auto-fix the findings flagged as Fixable (rewrites notes through bearcli)")
	rootCmd.AddCommand(lintCmd)
}

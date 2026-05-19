package main

import (
	"github.com/spf13/cobra"
)

// auditCmd runs a read-only sweep over every managed domain and prints
// the resulting Finding list grouped by domain+category. Equivalent to
// the legacy daemon's `--audit` mode: lists every note, evaluates the
// lint heuristics (broken-H1, malformed-canonical, unsafe-title,
// missing-canonical), and reports without writing.
//
// Exit codes:
//
//   - 0 — sweep completed; findings list rendered (zero or more rows)
//   - 1 — load error or partial sweep failure
//   - 130 — interrupted via SIGINT/SIGTERM
var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Scan every managed domain for lint findings (read-only)",
	Long: `Audit runs the lint heuristics over every atom under a managed
tag and prints a grouped report — domain → category → finding. This
is a read-only pass; no writes to Bear. Use 'noxctl lint --apply' to
auto-fix the findings flagged as Fixable.

Findings include broken-H1 titles, malformed canonical tag-lines,
unsafe x-callback titles, and missing canonical bootstrap on notes
that need it. Each fixable row is auto-resolvable by 'lint --apply';
non-fixable rows need manual review in Bear.`,
	RunE: runAudit,
}

// runAudit is the audit RunE. Audit is lint without --apply; both
// share runLintSweep (in preflight.go) which collapses the preflight
// + signal wiring into one place.
func runAudit(cmd *cobra.Command, _ []string) error {
	return runLintSweep(cmd, false)
}

func init() { rootCmd.AddCommand(auditCmd) }

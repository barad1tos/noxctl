package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/cli/lint"
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
// share runLintSweep which collapses the preflight + signal wiring
// into one place (and dodges dupl on the otherwise-identical bodies).
func runAudit(cmd *cobra.Command, _ []string) error {
	return runLintSweep(cmd, false)
}

// runLintSweep is the shared body for audit (apply=false) and lint
// (apply=lintApply). Splits the preflight error path from the
// sweep itself so the two CLI shims stay trivial.
func runLintSweep(cmd *cobra.Command, apply bool) error {
	domains, loadErr := domainsWithPreflight()
	if loadErr != nil {
		return loadErr
	}
	return runWithSignalContext(cmd, func(ctx context.Context) error {
		lint.Run(ctx, os.Stdout, domains, apply)
		return nil
	})
}

func init() { rootCmd.AddCommand(auditCmd) }

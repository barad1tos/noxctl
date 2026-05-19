package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear"
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

// runAudit is the audit RunE.
func runAudit(cmd *cobra.Command, _ []string) error {
	domains, loadErr := domainsWithPreflight()
	if loadErr != nil {
		return loadErr
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	findings := bear.AuditDomains(ctx, domains)

	// ctx cancellation maps to exit 130; we still print whatever findings
	// the partial sweep produced because audit is read-only.
	bear.PrintFindings(os.Stdout, findings, len(domains))

	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errInterrupted
	}
	return nil
}

func init() { rootCmd.AddCommand(auditCmd) }

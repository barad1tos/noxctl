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
canonical tag-lines) are rewritten through bearcli. Non-fixable
findings (unsafe-title, missing-canonical that requires user input)
are logged for manual review. Re-running on a clean corpus produces
no writes.`,
	RunE: runLint,
}

// runLint is the lint RunE.
func runLint(cmd *cobra.Command, _ []string) error {
	domains, loadErr := domainsWithPreflight()
	if loadErr != nil {
		return loadErr
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !lintApply {
		// Report-only path mirrors auditCmd. Kept inline rather than
		// delegating to runAudit so the --apply flag stays a single
		// switch on this command rather than a separate subcommand.
		findings := bear.AuditDomains(ctx, domains)
		bear.PrintFindings(os.Stdout, findings, len(domains))
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errInterrupted
		}
		return nil
	}

	bear.LintApplyDomains(ctx, domains)
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errInterrupted
	}
	return nil
}

func init() {
	lintCmd.Flags().BoolVar(&lintApply, "apply", false,
		"auto-fix the findings flagged as Fixable (rewrites notes through bearcli)")
	rootCmd.AddCommand(lintCmd)
}

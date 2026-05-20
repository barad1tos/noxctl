package cli

// lint.go implements the noxctl audit + lint subcommand bodies.
//
// cmd/noxctl/{audit,lint}.go reduce to cobra-wiring + flag parsing;
// this file owns the actual sweep — report-only (audit + `lint`
// without --apply) or auto-fix (lint --apply). Both subcommands
// share the same domain walk; the apply flag picks the action.

import (
	"context"
	"io"

	"github.com/barad1tos/noxctl/bear/audit"
	"github.com/barad1tos/noxctl/bear/domain"
)

// RunLint performs the lint sweep. When apply is false (audit mode or
// `lint` without --apply), it runs audit.Scan read-only and
// prints the grouped report to stdout. When apply is true, it runs
// audit.LintApplyDomains which rewrites Fixable rows through bearcli.
//
// ctx cancellation aborts the sweep at the next bearcli call. Both
// Scan and LintApplyDomains are log-and-continue on per-atom
// failures, so a partial sweep always renders whatever findings
// completed before the cancellation.
//
// stdout is parameterized so tests can capture the rendered findings;
// production wires os.Stdout in the CLI shim.
func RunLint(ctx context.Context, stdout io.Writer, domains []*domain.Domain, apply bool) {
	if apply {
		audit.LintApplyDomains(ctx, domains)
		return
	}
	findings := audit.Scan(ctx, domains)
	audit.PrintFindings(stdout, findings, len(domains))
}

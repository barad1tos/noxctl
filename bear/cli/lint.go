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
	"log"

	"github.com/barad1tos/noxctl/bear/audit"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
)

// RunLint performs the lint sweep. When apply is false (audit mode or
// `lint` without --apply), it runs audit.Scan read-only PLUS
// audit.ScanOrphanFamilies (corpus-level), merges and re-sorts the
// findings, then prints the grouped report. When apply is true, it
// runs audit.LintApplyDomains (rewrites Fixable rows through bearcli)
// AND then audit.ApplyOrphanFamilies (one `bearcli tag <id> orphans`
// per stray-family atom) — the apply order matters: per-domain auto-
// fix may rewrite atom bodies, so the orphan tag-add runs against the
// post-fix atom state.
//
// On orphan-scan failure, audit-mode logs and proceeds with just the
// per-domain findings (operator still sees most of the report). In
// apply-mode, orphan-scan failure aborts the orphan pass entirely
// (per-domain fixes have already landed; logging + early return is
// the cleanest recovery — the operator can re-run the lint sweep).
//
// ctx cancellation aborts the sweep at the next bearcli call. All
// four orchestrators are log-and-continue on per-atom failures, so a
// partial sweep always renders whatever findings completed before the
// cancellation.
//
// stdout is parameterized so tests can capture the rendered findings;
// production wires os.Stdout in the CLI shim.
func RunLint(ctx context.Context, stdout io.Writer, domains []*domain.Domain, apply bool) {
	domain.SetBearcliConcurrency(engine.DefaultBearcliConcurrency)
	if apply {
		audit.LintApplyDomains(ctx, domains)
		runApplyOrphanPass(ctx, domains)
		return
	}
	findings := audit.Scan(ctx, domains)
	findings = appendOrphanFindings(ctx, findings, domains)
	audit.PrintFindings(stdout, findings, len(domains))
}

// runApplyOrphanPass invokes the corpus orphan scan + tag-add chain.
// Empty-domain catalogs skip the scan entirely so an operator running
// `noxctl lint --apply` with a zero-domain config does not pay the
// bearcli list round-trip just to discover there is nothing to tag.
// Canceled contexts also short-circuit so a SIGINT during the per-
// domain auto-fix sweep does not leak into the orphan pass.
func runApplyOrphanPass(ctx context.Context, domains []*domain.Domain) {
	if len(domains) == 0 {
		return
	}
	if err := domain.CheckCtx(ctx); err != nil {
		return
	}
	orphanFindings, orphanErr := audit.ScanOrphanFamilies(ctx, domains)
	if orphanErr != nil {
		log.Printf("lint --apply: orphan scan failed: %v", orphanErr)
		return
	}
	tagged, failed := audit.ApplyOrphanFamilies(ctx, orphanFindings)
	if tagged > 0 || failed > 0 {
		log.Printf("lint --apply: orphan-family tagged=%d failed=%d", tagged, failed)
	}
}

// appendOrphanFindings runs the corpus orphan scan and merges its
// findings into the per-domain set, re-sorting in place. On scan
// failure the per-domain findings are returned unchanged so the
// audit report still renders — log-and-continue spirit matching
// audit.Scan's per-domain list failure handling.
//
// Empty-domain catalogs and canceled contexts skip the scan for the
// same short-circuit reasons as runApplyOrphanPass.
func appendOrphanFindings(ctx context.Context, findings []audit.Finding, domains []*domain.Domain) []audit.Finding {
	if len(domains) == 0 {
		return findings
	}
	if err := domain.CheckCtx(ctx); err != nil {
		return findings
	}
	orphanFindings, orphanErr := audit.ScanOrphanFamilies(ctx, domains)
	if orphanErr != nil {
		log.Printf("lint: orphan scan failed: %v", orphanErr)
		return findings
	}
	findings = append(findings, orphanFindings...)
	audit.SortFindings(findings)
	return findings
}

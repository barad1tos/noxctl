package cli

// lint.go implements the noxctl audit + lint subcommand bodies.
//
// cmd/noxctl/{audit,lint}.go reduce to cobra-wiring + flag parsing;
// this file owns the actual sweep — report-only (audit + `lint`
// without --apply) or auto-fix (lint --apply). Both subcommands
// share the same domain walk; the apply flag picks the action.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/barad1tos/noxctl/bear/audit"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
)

// ErrLintFailed signals that the lint sweep produced operator-actionable
// failures the cmd shim should surface as a non-zero exit code. Returned
// when apply-mode orphan tagging hit per-atom failures or when the
// corpus orphan scan could not complete. The cmd shim maps it to the
// Terraform-style exit-2 ("differences detected / action needed");
// runtime errors (ctx cancel, bearcli unreachable) come back as their
// own wrapped errors and map to the generic exit-1 path.
var ErrLintFailed = errors.New("noxctl lint: reported failures")

// RunLint performs the lint sweep. When apply is false (audit mode or
// `lint` without --apply), it runs audit.Scan read-only PLUS
// audit.ScanOrphanFamilies (corpus-level), merges and re-sorts the
// findings, then prints the grouped report. When apply is true, it
// runs audit.LintApplyDomains (rewrites Fixable rows through bearcli)
// AND then audit.ApplyOrphanFamilies (one `bearcli tags add <id>
// orphans` per stray-family atom) — the apply order matters:
// per-domain auto-fix may rewrite atom bodies, so the orphan tag-add
// runs against the post-fix atom state.
//
// On orphan-scan failure, audit-mode appends a synthetic finding to
// the report so the operator sees the gap inline AND returns the
// wrapped scan error so CI gates greping exit codes still catch the
// regression. In apply-mode, orphan-scan failure aborts the orphan
// pass (per-domain fixes have already landed; logging + returning
// the wrapped error lets the operator re-run the lint sweep with the
// failure context).
//
// ctx cancellation aborts the sweep at the next bearcli call. All
// four orchestrators are log-and-continue on per-atom failures, so a
// partial sweep always renders whatever findings completed before the
// cancellation. The returned error is non-nil when apply-mode hit
// per-atom failures (ErrLintFailed), or when the orphan scan could
// not complete in either mode (wrapped scan error). Audit mode
// returns nil when the scan ran clean — even if per-domain findings
// were emitted, those are operator-actionable but not a sweep
// failure.
//
// stdout is parameterized so tests can capture the rendered findings;
// production wires os.Stdout in the CLI shim.
func RunLint(ctx context.Context, stdout io.Writer, domains []*domain.Domain, apply bool) error {
	domain.SetBearcliConcurrency(engine.DefaultBearcliConcurrency)
	if apply {
		audit.LintApplyDomains(ctx, domains)
		return runApplyOrphanPass(ctx, domains)
	}
	findings := audit.Scan(ctx, domains)
	findings, scanErr := appendOrphanFindings(ctx, findings, domains)
	audit.PrintFindings(stdout, findings, len(domains))
	return scanErr
}

// runApplyOrphanPass invokes the corpus orphan scan + tag-add chain.
// Empty-domain catalogs skip the scan entirely so an operator running
// `noxctl lint --apply` with a zero-domain config does not pay the
// bearcli list round-trip just to discover there is nothing to tag.
// Canceled contexts also short-circuit so a SIGINT during the per-
// domain auto-fix sweep does not leak into the orphan pass.
//
// Returns ErrLintFailed when ApplyOrphanFamilies reports per-atom
// failures so the caller can surface the failure through the cmd
// shim. Scan failures come back as wrapped errors (runtime issue,
// not a finding-bearing failure). The cancel path returns ctx.Err
// wrapped with a per-pass prefix so the operator can tell the orphan
// pass was the one that aborted.
func runApplyOrphanPass(ctx context.Context, domains []*domain.Domain) error {
	if len(domains) == 0 {
		return nil
	}
	if err := domain.CheckCtx(ctx); err != nil {
		log.Printf("lint --apply: orphan pass skipped: %v", err)
		return fmt.Errorf("lint --apply orphan pass: %w", err)
	}
	orphanFindings, orphanErr := audit.ScanOrphanFamilies(ctx, domains)
	if orphanErr != nil {
		log.Printf("lint --apply: orphan scan failed: %v", orphanErr)
		return fmt.Errorf("lint --apply orphan scan: %w", orphanErr)
	}
	tagged, failed, applyErr := audit.ApplyOrphanFamilies(ctx, orphanFindings)
	log.Printf("lint --apply: orphan-family scanned=%d tagged=%d failed=%d",
		len(orphanFindings), tagged, failed)
	if applyErr != nil {
		return fmt.Errorf("lint --apply orphan tag-add: %w", applyErr)
	}
	if failed > 0 {
		return fmt.Errorf("%w: orphan tag-add failed for %d atom(s)", ErrLintFailed, failed)
	}
	return nil
}

// appendOrphanFindings runs the corpus orphan scan and merges its
// findings into the per-domain set, re-sorting in place. On scan
// failure a synthetic LintOrphanFamily Finding is appended in place
// of the missing scan output so the audit report itself signals the
// gap — operators redirecting `noxctl audit > report.txt` would
// otherwise see a normal-looking output with a silently empty orphan
// section. The scan error is also returned so the cmd shim can
// surface it through a non-zero exit code; CI gates that grep `$?`
// rather than parse the report still catch the regression.
//
// Empty-domain catalogs and canceled contexts skip the scan for the
// same short-circuit reasons as runApplyOrphanPass; both return
// (findings, nil) since neither is a scan failure.
func appendOrphanFindings(
	ctx context.Context,
	findings []audit.Finding,
	domains []*domain.Domain,
) ([]audit.Finding, error) {
	if len(domains) == 0 {
		return findings, nil
	}
	if err := domain.CheckCtx(ctx); err != nil {
		return findings, nil
	}
	orphanFindings, orphanErr := audit.ScanOrphanFamilies(ctx, domains)
	if orphanErr != nil {
		log.Printf("lint: orphan scan failed: %v", orphanErr)
		findings = append(findings, audit.Finding{
			Category: audit.LintOrphanFamily,
			Title:    "(orphan scan failed)",
			Detail:   flattenForReport(orphanErr.Error()),
			Fixable:  false,
		})
		audit.SortFindings(findings)
		return findings, fmt.Errorf("lint audit orphan scan: %w", orphanErr)
	}
	findings = append(findings, orphanFindings...)
	audit.SortFindings(findings)
	return findings, nil
}

// flattenForReport collapses newlines and tabs in a free-form error
// string into single spaces so the value renders on one line inside
// PrintFindings, which uses `Detail` in a single `%s` slot. Bearcli
// stack traces or multi-paragraph diagnostics would otherwise wrap
// across the column structure and break grep-style report parsers.
func flattenForReport(s string) string {
	r := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")
	return strings.TrimSpace(r.Replace(s))
}

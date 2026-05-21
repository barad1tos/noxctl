// Package audit owns the lint + scan pipeline — per-atom Finding
// detection (LintAtom in lints.go), corpus-wide orchestrators
// (Scan, LintApplyDomains in audit.go), and the untracked-
// tag corpus scanner (ScanUntracked in untracked.go). engine.Plan
// + cli/lint use this surface to drive `noxctl audit` and `noxctl
// lint --apply`.
package audit

// audit.go is the orchestrator entry: walks every domain, runs the
// per-atom lint pass via lints.go, and reports / auto-fixes the
// findings.

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

// LintAndAuditDomain runs the lint pass for one domain. Streams findings
// through the supplied channel — caller closes it after every domain has
// reported. listNotes is expensive (one bearcli round-trip), so the caller
// owns the listing and just hands the slice in.
func LintAndAuditDomain(d *domain.Domain, notes []domain.Note, sink func(Finding)) {
	for _, note := range notes {
		for _, finding := range LintAtom(d, note) {
			sink(finding)
		}
	}
}

// AutoFixDomain walks every atom in `notes` and applies AutoFixAtom to those
// with fixable findings, writing the result via bearcli overwrite. Returns
// counts (fixed, failed). Skips notes not currently treated as atomics.
//
// Use only after `--audit` has surfaced the findings — apply blindly without
// reviewing first risks overwriting user-curated body content if the heuristic
// drifts. The render-layer regen pass will rebuild canonicals on the next
// `--once` so AutoFixAtom only needs to leave the atom in a state where
// ParseMeta succeeds.
func AutoFixDomain(ctx context.Context, d *domain.Domain, notes []domain.Note) (fixed, failed int) {
	for _, note := range notes {
		if domain.IsAuxNote(d, note) {
			continue
		}
		newContent, changed := AutoFixAtom(d, note.Content)
		if !changed {
			continue
		}
		if err := bearcli.OverwriteWithRetry(ctx, note.ID, newContent); err != nil {
			d.Logf("lint fix %q failed: %v", note.Title, err)
			failed++
			continue
		}
		d.Logf("lint fixed: %s", note.Title)
		fixed++
	}
	return fixed, failed
}

// Scan is the audit-pass orchestrator: walks every domain in the
// supplied slice, lists its atomics via bearcli, runs LintAtom on each, and
// returns the consolidated finding set sorted by domain → category → title.
// Listing failures are logged but don't abort the pass — caller gets every
// reachable domain's findings.
//
// SIGINT/SIGTERM honors the operator's cancel intent: the ctx.Err check
// at the top of each iteration short-circuits before any bearcli I/O,
// so cancellation produces a deterministic zero-list-call exit instead
// of racing the bearcli pool semaphore. Without the explicit guard the
// per-domain listNotes call would still acquire the pool slot on the
// ~50% of select-races where the slot wins over ctx.Done().
//
//nolint:revive // public API; rename is breaking change for callers
func Scan(ctx context.Context, domains []*domain.Domain) []Finding {
	var findings []Finding
	for _, d := range domains {
		if err := domain.CheckCtx(ctx); err != nil {
			return findings
		}
		notes, err := bearcli.ListNotesForTag(ctx, d.Tag)
		if err != nil {
			log.Printf("audit: %s: list failed: %v", d.Tag, err)
			continue
		}
		LintAndAuditDomain(d, notes, func(finding Finding) {
			findings = append(findings, finding)
		})
	}
	SortFindings(findings)
	return findings
}

// PrintFindings writes a grouped, human-readable audit report to `w`.
// Findings are pre-sorted by Scan; this writer just adds section
// headers and a final tally line. Pure formatting — no IO outside `w`.
func PrintFindings(w io.Writer, findings []Finding, totalDomains int) {
	currentDomain := ""
	currentCategory := LintCategory("")
	for _, f := range findings {
		if f.DomainTag != currentDomain {
			currentDomain = f.DomainTag
			currentCategory = ""
			_, _ = fmt.Fprintf(w, "\n[%s]\n", f.DomainTag)
		}
		if f.Category != currentCategory {
			currentCategory = f.Category
			_, _ = fmt.Fprintf(w, "  %s:\n", f.Category)
		}
		fixable := ""
		if f.Fixable {
			fixable = "  (fixable)"
		}
		_, _ = fmt.Fprintf(w, "    %s — %s%s\n", f.Title, f.Detail, fixable)
	}
	_, _ = fmt.Fprintf(w, "\n%d findings across %d domains\n", len(findings), totalDomains)
}

// LogAuditFindings emits one log line per finding via `logf` (typically
// `log.Printf` from main). Used by the daemon's pre-regen audit pass so
// non-fixable findings (broken-h1, malformed-canonical without rebuild
// signal, unsafe-title) surface in the daemon log without the user having
// to invoke `--audit` separately. Findings should be pre-sorted by
// Scan; this writer just prints them. No-op on empty input.
func LogAuditFindings(findings []Finding, logf func(format string, args ...any)) {
	if len(findings) == 0 {
		return
	}
	fixable := 0
	for _, f := range findings {
		if f.Fixable {
			fixable++
		}
	}
	logf("audit: %d findings (%d auto-fixable via --lint --apply, %d need manual review)",
		len(findings), fixable, len(findings)-fixable)
	for _, f := range findings {
		marker := ""
		if f.Fixable {
			marker = " [fixable]"
		}
		logf("audit: [%s/%s] %s — %s%s", f.DomainTag, f.Category, f.Title, f.Detail, marker)
	}
}

// LintApplyDomains is the auto-fix orchestrator: walks every domain, lists
// its atomics, and applies AutoFixAtom + bearcli overwrite for the fixable
// findings. Idempotent — re-running on a clean corpus is a no-op. Listing
// failures are logged but don't abort the pass.
//
// Honors SIGINT/SIGTERM the same way Scan does — the ctx.Err
// guard at the top of each iteration short-circuits before any bearcli
// I/O, so a canceled sweep stops deterministically instead of racing the
// pool semaphore for one more list+overwrite round.
func LintApplyDomains(ctx context.Context, domains []*domain.Domain) {
	for _, d := range domains {
		if err := domain.CheckCtx(ctx); err != nil {
			return
		}
		notes, err := bearcli.ListNotesForTag(ctx, d.Tag)
		if err != nil {
			log.Printf("lint --apply: %s: list failed: %v", d.Tag, err)
			continue
		}
		fixed, failed := AutoFixDomain(ctx, d, notes)
		if fixed > 0 || failed > 0 {
			d.Logf("lint --apply: %d fixed, %d failed", fixed, failed)
		}
	}
}

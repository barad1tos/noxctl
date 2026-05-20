package bear

// Lint audit orchestrator: walks every domain, runs the per-atom lint
// pass via lint_atom.go, and reports / auto-fixes the findings. Split
// from lint.go to keep CLI-facing orchestration separate from the
// per-atom detection logic.

import (
	"context"
	"fmt"
	"io"
	"log"
)

// LintAndAuditDomain runs the lint pass for one domain. Streams findings
// through the supplied channel — caller closes it after every domain has
// reported. listNotes is expensive (one bearcli round-trip), so the caller
// owns the listing and just hands the slice in.
func LintAndAuditDomain(d *Domain, notes []Note, sink func(Finding)) {
	for _, note := range notes {
		for _, finding := range d.LintAtom(note) {
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
func AutoFixDomain(ctx context.Context, d *Domain, notes []Note) (fixed, failed int) {
	for _, note := range notes {
		if d.skipNote(note) {
			continue
		}
		newContent, changed := d.AutoFixAtom(note.Content)
		if !changed {
			continue
		}
		if err := overwriteWithRetry(ctx, note.ID, newContent); err != nil {
			d.Logf("lint fix %q failed: %v", note.Title, err)
			failed++
			continue
		}
		d.Logf("lint fixed: %s", note.Title)
		fixed++
	}
	return fixed, failed
}

// AuditDomains is the audit-pass orchestrator: walks every domain in the
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
func AuditDomains(ctx context.Context, domains []*Domain) []Finding {
	var findings []Finding
	for _, domain := range domains {
		if err := CheckCtx(ctx); err != nil {
			return findings
		}
		notes, err := domain.listNotes(ctx)
		if err != nil {
			log.Printf("audit: %s: list failed: %v", domain.Tag, err)
			continue
		}
		LintAndAuditDomain(domain, notes, func(f Finding) {
			findings = append(findings, f)
		})
	}
	SortFindings(findings)
	return findings
}

// PrintFindings writes a grouped, human-readable audit report to `w`.
// Findings are pre-sorted by AuditDomains; this writer just adds section
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
// AuditDomains; this writer just prints them. No-op on empty input.
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
// Honors SIGINT/SIGTERM the same way AuditDomains does — the ctx.Err
// guard at the top of each iteration short-circuits before any bearcli
// I/O, so a canceled sweep stops deterministically instead of racing the
// pool semaphore for one more list+overwrite round.
func LintApplyDomains(ctx context.Context, domains []*Domain) {
	for _, domain := range domains {
		if err := CheckCtx(ctx); err != nil {
			return
		}
		notes, err := domain.listNotes(ctx)
		if err != nil {
			log.Printf("lint --apply: %s: list failed: %v", domain.Tag, err)
			continue
		}
		fixed, failed := AutoFixDomain(ctx, domain, notes)
		if fixed > 0 || failed > 0 {
			domain.Logf("lint --apply: %d fixed, %d failed", fixed, failed)
		}
	}
}

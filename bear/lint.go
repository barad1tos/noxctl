package bear

import (
	"context"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
)

// Lint pass — detects atomic notes whose state would either silently break
// rendering (titles needing URL form) or persist as data residue across
// regen cycles (multi-canonical lines, orphan tag tokens, broken H1 from
// prior failed migrations). Pairs with a CLI surface in main.go:
//
//	regen-watchd --audit → scan & report, no writes
//	regen-watchd --lint --apply → auto-fix unambiguous findings
//
// Auto-fix is conservative: only multi-canonical (drop everything except the
// first canonical line) and orphan-tag (strip standalone `#<top>/<sub>` tokens
// outside the canonical line) are applied. Broken-H1 is reported but never
// auto-fixed — the original title is unrecoverable from the corrupted body
// alone, so the user has to supply it.

// LintCategory enumerates the classes of atom-level data anomalies the lint
// pass detects. New categories slot in alongside the existing ones; the
// audit reporter groups findings by category.
type LintCategory string

const (
	// LintBrokenH1 — note title starts with the pipe/asterisk fragments of a
	// canonical header line; the original H1 was overwritten by an earlier
	// migration script that incorrectly treated a body line as the H1.
	LintBrokenH1 LintCategory = "broken-h1"
	// LintMultiCanonical — header zone has ≥2 canonical-shape lines. ParseMeta
	// picks the first one, so the rest is silent residue. Auto-fixable: keep
	// the first canonical, strip the rest.
	LintMultiCanonical LintCategory = "multi-canonical"
	// LintOrphanTag — body has a standalone `#<top>` or `#<top>/<sub>` token
	// outside the canonical line. Bear's tag tree is built from any such
	// token, so duplicate tags cause sidebar noise. Auto-fixable: strip the
	// orphan token, keep the canonical.
	LintOrphanTag LintCategory = "orphan-tag"
	// LintUnsafeTitle — title contains `|`, `]`, or `[` which break Bear's
	// `[[wikilink]]` parsing. Render layer (AtomicWikilink) emits URL-form
	// links automatically, so no atom-level fix is needed; the finding is
	// informational so the user can rename if desired.
	LintUnsafeTitle LintCategory = "unsafe-title"
	// LintMalformedCanonical — orphan `#<top>` token exists but no recognized
	// canonical-shape line is present. Likely a malformed canonical that
	// missed the parser (e.g. extra tokens between `#tag` and ` | `). Not
	// auto-fixable: scrubbing the orphan would eat the tag off the malformed
	// canonical too. Surface for manual review.
	LintMalformedCanonical LintCategory = "malformed-canonical"
	// LintUntracked — atomic note carries a tag whose top-level segment is
	// NOT in the closed catalog of TOML-managed domains. Emitted by the
	// residue scan (bear/residue.go,), NOT by per-atom LintAtom.
	// Informational: noxctl deliberately does not touch unmanaged tags —
	// CONTEXT D-02 separates residue from drift, and residue does NOT
	// contribute to plan exit-code 2.
	LintUntracked LintCategory = "untracked"
)

// Finding is one anomaly detected by the lint pass. Multiple findings can
// fire for one atom (e.g. broken-h1 + multi-canonical co-occur after a
// botched migration).
type Finding struct {
	DomainTag string
	NoteID    string
	Title     string
	Category  LintCategory
	Detail    string
	Fixable   bool
}

// LintAtom inspects one atom and returns every finding that fires. Empty
// slice when the atom is clean. Skips notes that the domain treats as
// non-atomic (master, Tier-2 hubs).
func (d *Domain) LintAtom(n Note) []Finding {
	if d.skipNote(n) {
		return nil
	}
	var out []Finding

	if titleFinding, ok := titleLevelFinding(d, n); ok {
		out = append(out, titleFinding)
	}

	canonicalLines := findCanonicalLineIndices(n.Content, d.Tag)
	if len(canonicalLines) > 1 {
		out = append(out, Finding{
			DomainTag: d.Tag, NoteID: n.ID, Title: n.Title,
			Category: LintMultiCanonical,
			Detail:   fmt.Sprintf("%d canonical-shape lines; auto-fix keeps the first", len(canonicalLines)),
			Fixable:  true,
		})
	}

	if findOrphanTagLines(n.Content, d.Tag, canonicalLines) {
		if len(canonicalLines) == 0 {
			_, _, reconstructible := reconstructFirstCanonicalLine(n.Content, d.Tag)
			detail := fmt.Sprintf("`#%s` token present but no canonical-shape line; manual review", d.Tag)
			if reconstructible {
				detail = fmt.Sprintf("malformed canonical for `#%s`; auto-rebuild from tag + wikilink available", d.Tag)
			}
			out = append(out, Finding{
				DomainTag: d.Tag, NoteID: n.ID, Title: n.Title,
				Category: LintMalformedCanonical,
				Detail:   detail,
				Fixable:  reconstructible,
			})
		} else {
			out = append(out, Finding{
				DomainTag: d.Tag, NoteID: n.ID, Title: n.Title,
				Category: LintOrphanTag,
				Detail:   fmt.Sprintf("standalone `#%s` or `#%s/<sub>` outside canonical line", d.Tag, d.Tag),
				Fixable:  true,
			})
		}
	}

	return out
}

// AutoFixAtom applies the fixable findings for an atom. Returns the new
// body content and `true` when at least one fix was applied; `false` means
// either no findings or only non-fixable ones (broken-h1, unsafe-title).
//
// Three paths:
// - Multi-canonical → keep the first canonical line, drop the rest.
// - Orphan-tag with a recognized canonical → strip orphan tokens off
// the non-canonical lines.
// - Malformed-canonical (orphan tag + no canonical recognized) → try
// to reconstruct the canonical from the tag-token + wikilink that
// already live on the same line. When that succeeds, the rebuilt
// line replaces the malformed one and the rest of the body is
// scrubbed normally; when reconstruction fails (no wikilink, no
// pipe, ambiguous signal), bail and surface for manual review.
//
// Idempotent: running twice on a fixed atom is a no-op. The render-layer
// downstream will rewrite the canonical anyway, so AutoFixAtom only needs
// to leave the body in a state where ParseMeta succeeds and no orphan tags
// remain — exact whitespace doesn't matter.
func (d *Domain) AutoFixAtom(content string) (string, bool) {
	canonicalIndices := findCanonicalLineIndices(content, d.Tag)
	hasOrphan := findOrphanTagLines(content, d.Tag, canonicalIndices)
	multi := len(canonicalIndices) > 1

	if !multi && !hasOrphan {
		return content, false
	}

	if hasOrphan && len(canonicalIndices) == 0 {
		spliced, idx, ok := spliceReconstructedCanonical(content, d.Tag)
		if !ok {
			return content, false
		}
		content = spliced
		canonicalIndices = []int{idx}
	}

	rebuilt := scrubLinesAroundCanonical(content, d.Tag, canonicalIndices)

	// Drop trailing blank lines that pile up after stripping orphans.
	for len(rebuilt) > 0 && strings.TrimSpace(rebuilt[len(rebuilt)-1]) == "" {
		rebuilt = rebuilt[:len(rebuilt)-1]
	}
	rebuilt = append(rebuilt, "")
	return strings.Join(rebuilt, "\n"), true
}

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

// SortFindings orders findings by domain → category → title for stable
// audit output. Mutates in place.
func SortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].DomainTag != findings[j].DomainTag {
			return findings[i].DomainTag < findings[j].DomainTag
		}
		if findings[i].Category != findings[j].Category {
			return findings[i].Category < findings[j].Category
		}
		return findings[i].Title < findings[j].Title
	})
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

// titleLevelFinding consolidates the broken-H1 and unsafe-title checks.
// Returns (Finding, true) when the title triggers either; (zero, false)
// when the title is clean. broken-H1 wins over unsafe-title because a
// title with a leading pipe is structurally corrupt — the URL-form
// fallback would render fine but the title itself is wrong.
func titleLevelFinding(d *Domain, n Note) (Finding, bool) {
	var category LintCategory
	var detail string
	switch {
	case titleLooksBroken(n.Title):
		category = LintBrokenH1
		detail = "title starts with canonical-line fragment; original H1 lost"
	case titleNeedsURLForm(n.Title):
		category = LintUnsafeTitle
		detail = "title contains | ] [ — wikilink emitted as bear:// URL by render"
	default:
		return Finding{}, false
	}
	return Finding{
		DomainTag: d.Tag, NoteID: n.ID, Title: n.Title,
		Category: category, Detail: detail,
	}, true
}

// titleLooksBroken reports whether a title carries the residue of a previous
// canonical-header overwrite. A title starting with `|` or `# ` is the
// signature shape: the H1 line was `# ` followed by a leftover canonical
// fragment, and Bear took everything after the `# ` as the title.
func titleLooksBroken(title string) bool {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, "# ")
}

// findCanonicalLineIndices returns line indices of every line that matches
// the canonical-header shape for this domain's tag. Used by both the linter
// (multi-canonical detection) and the auto-fixer (drop all but the first).
func findCanonicalLineIndices(content, tag string) []int {
	var out []int
	for idx, line := range strings.Split(content, "\n") {
		if isCanonicalLineForTag(line, tag) {
			out = append(out, idx)
		}
	}
	return out
}

// isCanonicalLineForTag mirrors the migration script's classifier — line
// must start with `#<tag> |` or `#<tag>/` AND contain `[[` AND ` | ` to
// qualify as canonical-shape. Mid-text mentions of `#tag/sub` are excluded.
func isCanonicalLineForTag(line, tag string) bool {
	stripped := strings.TrimSpace(line)
	if !strings.Contains(stripped, "[[") || !strings.Contains(stripped, " | ") {
		return false
	}
	prefixSpace := "#" + tag + " |"
	prefixSlash := "#" + tag + "/"
	return strings.HasPrefix(stripped, prefixSpace) || strings.HasPrefix(stripped, prefixSlash)
}

// findOrphanTagLines reports whether any non-canonical line in `content`
// contains a `#<tag>` or `#<tag>/<sub>` token. canonicalIndices marks lines
// that legitimately carry the tag; everything else is suspect.
func findOrphanTagLines(content, tag string, canonicalIndices []int) bool {
	canonical := make(map[int]struct{}, len(canonicalIndices))
	for _, idx := range canonicalIndices {
		canonical[idx] = struct{}{}
	}
	prefix := "#" + tag
	for idx, line := range strings.Split(content, "\n") {
		if _, isCanonical := canonical[idx]; isCanonical {
			continue
		}
		// Token-boundary check: avoid matching `[[#tag/foo]]` or in-text mentions.
		for token := range strings.FieldsSeq(line) {
			if token == prefix || strings.HasPrefix(token, prefix+"/") {
				return true
			}
		}
	}
	return false
}

// spliceReconstructedCanonical wraps reconstructFirstCanonicalLine and
// performs the line-level splice — replaces the malformed line with the
// rebuilt canonical and returns the new content + new canonical line index.
// Returns ("", 0, false) when reconstruction wasn't possible (caller bails).
func spliceReconstructedCanonical(content, tag string) (string, int, bool) {
	rebuilt, idx, ok := reconstructFirstCanonicalLine(content, tag)
	if !ok {
		return "", 0, false
	}
	lines := strings.Split(content, "\n")
	lines[idx] = rebuilt
	return strings.Join(lines, "\n"), idx, true
}

// scrubLinesAroundCanonical applies the line-by-line scrub: drop every
// canonical line except the first, strip orphan top-tag tokens from every
// non-canonical line, leave the surviving canonical untouched. Returns the
// rebuilt slice — caller joins with "\n".
func scrubLinesAroundCanonical(content, tag string, canonicalIndices []int) []string {
	lines := strings.Split(content, "\n")
	keepFirstCanonical := -1
	if len(canonicalIndices) > 0 {
		keepFirstCanonical = canonicalIndices[0]
	}
	dropCanonicalSet := make(map[int]struct{}, len(canonicalIndices))
	for _, idx := range canonicalIndices {
		if idx != keepFirstCanonical {
			dropCanonicalSet[idx] = struct{}{}
		}
	}
	var rebuilt []string
	for idx, line := range lines {
		if _, dropped := dropCanonicalSet[idx]; dropped {
			continue
		}
		if idx != keepFirstCanonical {
			rebuilt = append(rebuilt, stripOrphanTopTags(line, tag))
			continue
		}
		rebuilt = append(rebuilt, line)
	}
	return rebuilt
}

// reconstructFirstCanonicalLine scans `content` for a malformed line that
// carries enough signal to be a canonical-shape line: a `#<tag>` (or
// `#<tag>/<sub>`) token, a ` | ` separator, and at least one `[[Target]]`
// wikilink. When all three are present on the same line, returns the
// rebuilt canonical (`<tag-token> | [[Target]]`), the line index, and true.
// Returns ("", 0, false) when no line passes the check — the caller falls
// back to manual-review mode.
//
// Conservative on purpose: only reconstructs from the FIRST matching line
// to avoid arbitrating between multiple ambiguous lines. The 3rd pipe-
// segment (section / hub-routed extras) is dropped — daemon's render
// layer will repopulate it from the recanonicalized atom body.
func reconstructFirstCanonicalLine(content, tag string) (string, int, bool) {
	tagPrefix := "#" + tag
	for idx, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, " | ") || !strings.Contains(line, "[[") {
			continue
		}
		var tagToken string
		for token := range strings.FieldsSeq(line) {
			if token == tagPrefix || strings.HasPrefix(token, tagPrefix+"/") {
				tagToken = token
				break
			}
		}
		if tagToken == "" {
			continue
		}
		openIdx := strings.Index(line, "[[")
		closeIdx := strings.Index(line[openIdx:], "]]")
		if openIdx < 0 || closeIdx < 0 {
			continue
		}
		wikilink := line[openIdx : openIdx+closeIdx+2]
		return tagToken + " | " + wikilink, idx, true
	}
	return "", 0, false
}

// stripOrphanTopTags removes standalone `#<tag>` and `#<tag>/<sub>` tokens
// from a line; preserves all other content. Used by AutoFixAtom on lines
// other than the surviving canonical.
func stripOrphanTopTags(line, tag string) string {
	tokens := strings.Fields(line)
	if len(tokens) == 0 {
		return line
	}
	prefix := "#" + tag
	kept := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token == prefix || strings.HasPrefix(token, prefix+"/") {
			continue
		}
		kept = append(kept, token)
	}
	if len(kept) == len(tokens) {
		return line
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, " ")
}

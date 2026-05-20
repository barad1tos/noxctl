package bear

// Lint atom-level pass: per-atom anomaly detection (LintAtom) and the
// auto-fix logic (AutoFixAtom) plus all private helpers they use. Split
// from lint.go to keep the audit/apply orchestrator and the per-atom
// logic at separate file scopes.

import (
	"fmt"
	"strings"
)

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

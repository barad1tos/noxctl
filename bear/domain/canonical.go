package domain

// Atomic-body parsing + canonical rendering — the round-trip between
// raw note bodies and the structured AtomicParts representation,
// plus the bearcli overwrite path that stamps fresh canonical lines
// on atoms. parseAtomicContent / renderAtomicCanonical /
// upsertAtomicBacklink form a tight cluster with their own helpers
// (atomicParseState, isEmptyH1).

import (
	"context"
	"fmt"
	"strings"
)

type atomicParseState struct {
	seenH1       bool
	seenTagLine  bool // flipped once consumeHeader claims a header-shape line or `---`
	skipAuthorH2 bool
	stripAuthor  bool // domain toggle: when false, never treat "## <author>" as legacy marker
}

// consumeH1 captures the first H1 into p.h1Line. Returns true if consumed.
func (s *atomicParseState) consumeH1(trimmed string, p *AtomicParts) bool {
	if s.seenH1 || !strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") {
		return false
	}
	p.H1Line = trimmed
	s.seenH1 = true
	return true
}

// consumeHeader consumes anything that lives in the header zone: separator,
// header-shape lines, optionally the legacy `## Author` H2 plus its trailing
// blanks (only when stripAuthor=true). `family` scopes which tags count as
// canonical (and therefore get filtered out of ExtraTags) — see
// collectExtraTags doc.
func (s *atomicParseState) consumeHeader(trimmed, authorH2Marker, family string, p *AtomicParts) bool {
	if trimmed == "---" {
		s.seenTagLine = true
		return true
	}
	if isHeaderLine(trimmed) {
		if section := extractSectionFromHeaderLine(trimmed); section != "" {
			p.Section = section
		}
		p.ExtraTags = append(p.ExtraTags, collectExtraTags(trimmed, family)...)
		s.seenTagLine = true
		return true
	}
	if s.stripAuthor && trimmed == authorH2Marker {
		s.skipAuthorH2 = true
		return true
	}
	if s.skipAuthorH2 && trimmed == "" {
		return true
	}
	s.skipAuthorH2 = false
	return false
}

// consumePreamble captures non-tag-line content that lives between the
// H1 and the first canonical header line. Production render paths
// hoist this slice into the body zone via hoistPreambleToBody, so
// preamble content always lands below `---` after canonicalization —
// any layout where the user (or Bear's new-note URL template cursor)
// put body content into the preamble zone gets normalized to the
// canonical contract `H1 / tag-line / --- / body` on the next regen
// tick. Dispatch order is H1 → header → preamble → leading-blank →
// body; preamble runs only AFTER consumeHeader has had its chance to
// claim header-shape lines.
//
// Rejects lines that LOOK like canonical-line debris — `#<token> |...`
// shapes that failed isHeaderLine (e.g. `#development/✱ Daily |...`
// where the bucket name carried a space and broke segment[0]'s
// tag-only check). Claiming such lines as preamble would re-emit them
// on every regen tick, growing the body without bound — see the May
// 2026 accumulation cascade triggered by a foreign-tag escape that
// left `[[✱ Daily]]` debris in the body, which DetectAuthor then
// mis-identified as a bucket name, which the renderer emitted as
// `#development/✱ Daily |...`, which preamble then preserved...
// Sending them to BodyLines instead at least keeps the user's
// historical content visible below `---` for manual cleanup.
func (s *atomicParseState) consumePreamble(trimmed string, p *AtomicParts) bool {
	if !s.seenH1 || s.seenTagLine {
		return false
	}
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, " | ") {
		return false
	}
	p.PreambleLines = append(p.PreambleLines, trimmed)
	return true
}

// consumeLeadingBlank drops blank lines preceding the first body line.
func (s *atomicParseState) consumeLeadingBlank(trimmed string, p *AtomicParts) bool {
	return len(p.BodyLines) == 0 && trimmed == ""
}

// parseAtomicContent destructures an atomic note's content into header H1,
// preserved extra tags, optional section path, and body lines.
//
// Each line dispatches to one of three consumers via switch — explicit short-
// circuit semantics. consume* methods mutate `s` and `p` as side effects; the
// switch documents that order matters (H1 before header before blank-skip).
func (d *Domain) parseAtomicContent(content, author string) AtomicParts {
	authorH2Marker := "## " + author
	family := strings.SplitN(d.Tag, "/", 2)[0]
	var parts AtomicParts
	state := atomicParseState{stripAuthor: d.StripLegacyAuthorH2}
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case state.consumeH1(trimmed, &parts):
		case state.consumeHeader(trimmed, authorH2Marker, family, &parts):
		case state.consumePreamble(trimmed, &parts):
		case state.consumeLeadingBlank(trimmed, &parts):
		default:
			parts.BodyLines = append(parts.BodyLines, line)
		}
	}
	return parts
}

// renderAtomicCanonical produces the canonical atomic body shape:
//
//	# Title
//	<preamble lines, if any>
//	#<tag> [extra tags] | [[Backlink]] [| section]
//	---
//
//	<text>
//
// The leading `#<tag>` token comes from d.ResolveCanonicalTag(bucket) so domains
// that preserve sub-tags can emit `#<top>/<bucket>` per atomic without
// touching the rest of the canonicalization flow. Callers must invoke
// `hoistPreambleToBody` on the parsed `AtomicParts` before threading
// `contentBody` here — the canonical shape is `H1 / tag-line / --- /
// body` with no preamble zone.
func (d *Domain) renderAtomicCanonical(
	h1Line string,
	extraTags []string,
	bucket,
	backlink,
	section,
	contentBody string,
) string {
	canonicalTag := d.ResolveCanonicalTag(bucket)
	tagLine := canonicalTag
	if len(extraTags) > 0 {
		tagLine = canonicalTag + " " + strings.Join(extraTags, " ")
	}
	suffix := ""
	if section != "" {
		suffix = " | " + section
	}
	suffix += NewNoteURLFromDomain(d).Emit()
	var b strings.Builder
	b.WriteString(h1Line + "\n")
	_, _ = fmt.Fprintf(&b, "%s | %s%s\n---\n\n", tagLine, backlink, suffix)
	if contentBody != "" {
		b.WriteString(contentBody + "\n")
	}
	return b.String()
}

// upsertAtomicBacklink restructures one atomic note into canonical header form.
// Idempotent: returns ("", nil) when content already matches the canonical
// render. On a successful rewrite returns a human-readable summary; on failure
// returns ("", err) so the caller can aggregate failure counts.
//
// H1 handling (spec components 2 + 8): when the atom's body lacks a
// recognized H1 (or carries an empty `# ` H1), the daemon stamps
// `# <NOW>` from the package-level time seam. Bear then derives the
// displayed title from the H1. The legacy noteTitle fallback is gone —
// every canonicalized atom carries a datetime H1, eliminating the
// `# #tag` recursive-corruption class.
//
// Idempotency comparison strips the trailing `[Нова нотатка](bear://...)`
// segment from both sides — its label/URL drift across regen cycles would
// otherwise force a no-op write per tick.
func (d *Domain) upsertAtomicBacklink(
	ctx context.Context,
	noteID,
	noteTitle,
	bucket,
	content string,
) (string, error) {
	parts := d.parseAtomicContent(content, bucket)
	if parts.H1Line == "" || isEmptyH1(parts.H1Line) {
		parts.H1Line = "# " + NowForNewNoteLink().Format(H1DatetimeFormat)
	}
	hoistPreambleToBody(&parts)
	contentBody := strings.Trim(strings.Join(parts.BodyLines, "\n"), "\n ")
	desired := d.renderAtomicCanonical(
		parts.H1Line, parts.ExtraTags, bucket,
		d.backlinkFor(bucket), d.sectionFor(bucket, parts), contentBody,
	)

	if equalIgnoringNewNoteLinkStrict(desired, content) {
		return "", nil
	}
	if err := overwriteWithRetry(ctx, noteID, desired); err != nil {
		return "", fmt.Errorf("upsertAtomicBacklink %q: %w", noteTitle, err)
	}
	return fmt.Sprintf("%s → restructured", noteTitle), nil
}

// isEmptyH1 reports whether an H1 line carries no meaningful content
// (e.g. `# ` after trimming whitespace). Empty H1s are not user intent —
// the daemon overwrites them with a datetime stamp per spec component 7.
func isEmptyH1(line string) bool {
	return strings.TrimSpace(strings.TrimPrefix(line, "#")) == ""
}

// hoistPreambleToBody relocates content the parser placed between H1
// and the canonical tag-line into the body zone (below `---`). Mirrors
// the bootstrap behavior across the per-domain regen path so notes
// authored via Bear's new-note URL template — where Bear's cursor
// frequently lands above the canonical line — get normalized to the
// canonical contract `H1 / tag-line / --- / body` on the next regen
// tick. Idempotent: no-ops when PreambleLines is empty.
func hoistPreambleToBody(p *AtomicParts) {
	if len(p.PreambleLines) == 0 {
		return
	}
	p.BodyLines = append(append([]string{}, p.PreambleLines...), p.BodyLines...)
	p.PreambleLines = nil
}

// RenderAtomicCanonicalForTest exposes the in-memory rendering path of
// upsertAtomicBacklink without the bearcli round-trip. Tests use it to
// assert canonical body shape (H1 stamping, preamble hoist,
// canonical-line composition) deterministically. The noteTitle arg is
// kept in the signature so tests document the historical Bear-side
// title input — the new datetime-stamp path doesn't consult it.
func RenderAtomicCanonicalForTest(t interface{ Helper() }, d *Domain, noteTitle, bucket, content string) string {
	t.Helper()
	_ = noteTitle
	parts := d.parseAtomicContent(content, bucket)
	if parts.H1Line == "" || isEmptyH1(parts.H1Line) {
		parts.H1Line = "# " + NowForNewNoteLink().Format(H1DatetimeFormat)
	}
	hoistPreambleToBody(&parts)
	contentBody := strings.Trim(strings.Join(parts.BodyLines, "\n"), "\n ")
	return d.renderAtomicCanonical(
		parts.H1Line, parts.ExtraTags, bucket,
		d.backlinkFor(bucket), d.sectionFor(bucket, parts), contentBody,
	)
}

// RenderCanonicalForBootstrap returns the canonical body form for a note
// that is being tagged for the first time (auto-tag default flow) or
// being escaped from quicknote into a permanent domain (foreign-tag
// escape flow). Reuses parseAtomicContent + renderAtomicCanonical so the
// output is byte-equivalent to what upsertAtomicBacklink would produce
// on the next regen pass — letting the subsequent cycle no-op via
// equalIgnoringNewNoteLink. Bucket selection uses the domain's
// UnknownBucket since fresh or just-escaped atoms carry no
// canonical-header section yet — domains with per-bucket routing
// (poetry, articles, …) re-bucket on the next full regen via ParseMeta
// + cross-domain moves.
//
// Body lines that parseAtomicContent captured as preamble (free-form
// content that the user typed before any canonical tag-line existed)
// are MOVED BELOW the tag-line + `---` separator via
// `hoistPreambleToBody`. The same hoist now runs in the per-domain
// regen path (`upsertAtomicBacklink`), so the canonical contract is
// uniform across both: no content survives between H1 and tag-line.
func (d *Domain) RenderCanonicalForBootstrap(existingContent string) string {
	parts := d.parseAtomicContent(existingContent, d.UnknownBucket)
	if parts.H1Line == "" || isEmptyH1(parts.H1Line) {
		parts.H1Line = "# " + NowForNewNoteLink().Format(H1DatetimeFormat)
	}
	hoistPreambleToBody(&parts)
	contentBody := strings.Trim(strings.Join(parts.BodyLines, "\n"), "\n ")
	return d.renderAtomicCanonical(
		parts.H1Line, parts.ExtraTags, d.UnknownBucket,
		d.backlinkFor(d.UnknownBucket), d.sectionFor(d.UnknownBucket, parts), contentBody,
	)
}

package bear

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// HasForeignQuicknoteTag reports whether `tags` represents an atom that
// is currently in the `quicknote/*` flow AND carries at least one
// non-quicknote tag. The user uses this signal to permanently exit the
// quicknote auto-flow — adding e.g. `#archive` or `#important` to a
// `#quicknote/daily` note flags the atom for escape.
//
// Returns false when no `quicknote/*` tag is present (nothing to escape
// from) or when every tag's top-level segment is `quicknote` (still
// fully inside the auto-flow).
func HasForeignQuicknoteTag(tags []string) bool {
	hasQuicknote := false
	hasForeign := false
	for _, tag := range tags {
		if topLevelSegment(tag) == "quicknote" {
			hasQuicknote = true
		} else {
			hasForeign = true
		}
	}
	return hasQuicknote && hasForeign
}

// topLevelSegment returns the part of `tag` before the first `/`. The
// leading `#` is stripped defensively because bearcli list returns
// tags WITH a hash prefix (`"#quicknote/daily"`) — older comments here
// claimed otherwise; verified via live `bearcli list --tag` +
// `--location notes` in 2026-05. Without the strip every predicate
// downstream (HasForeignQuicknoteTag, hasQuicknoteTag, residue
// managed-roots lookup) was always returning false against real
// bearcli output, silently neutering the foreign-tag escape pre-pass.
// Internal callers passing bare `d.Tag` are unaffected — TrimPrefix
// is a no-op when `#` isn't present.
func topLevelSegment(tag string) string {
	head, _, _ := strings.Cut(strings.TrimPrefix(tag, "#"), "/")
	return head
}

// SubstituteQuicknoteInBody surgically replaces every `#quicknote/<sub>`
// token in `body` with `replacement` (typically the foreign tag the user
// signaled by dragging the note onto a different sidebar tag). Pure
// token-for-token swap: surrounding whitespace, line breaks, and any
// other body content are preserved byte-for-byte. The canonical-line
// structure that follows the tag (` | [[hub]] | [Нова нотатка](url)`)
// stays intact in place; downstream domain canonicalization rewrites
// the backlink and new-note URL on its own pass.
//
// Token delimiters: a `#quicknote/<sub>` token ends at the first
// whitespace, end-of-string, `|` (pipe — canonical-line separator), or
// `)` (URL parenthesis). Anything between `#quicknote/` and the next
// delimiter is treated as `<sub>` and discarded — only `replacement`
// remains.
//
// Dedup is explicitly NOT done here: if the user drags a note onto a
// sidebar tag, Bear may add the new tag as a standalone body line. The
// daemon-side substitution then produces a second mention in the
// canonical line position. Both stay; the downstream RunRegen pipeline
// collapses redundant header-shape lines naturally (consumeHeader
// claims them, renderer emits one canonical row).
func SubstituteQuicknoteInBody(body, replacement string) string {
	const prefix = "#quicknote/"
	var b strings.Builder
	b.Grow(len(body))
	rest := body
	for {
		i := strings.Index(rest, prefix)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i])
		rest = rest[i:]
		end := strings.IndexAny(rest, " \t\n|)")
		if end < 0 {
			end = len(rest)
		}
		b.WriteString(replacement)
		rest = rest[end:]
	}
}

// FindForeignTagInBody scans `body` for the first standalone tag-only
// line whose top-level segment differs from "quicknote". Returns the
// raw token (with leading `#`) so callers can pass it straight into
// SubstituteQuicknoteInBody. Empty string when no such line exists.
//
// Bear inserts a fresh `#<tag>` line into the body when the user drags
// a note onto a sidebar tag. This helper extracts that token so the
// foreign-tag escape can substitute it into the existing quicknote
// canonical line.
func FindForeignTagInBody(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.Contains(trimmed, " | ") {
			continue
		}
		tokens := strings.Fields(trimmed)
		if len(tokens) != 1 {
			continue
		}
		tok := tokens[0]
		if !strings.HasPrefix(tok, "#") {
			continue
		}
		if topLevelSegment(tok) == "quicknote" {
			continue
		}
		return tok
	}
	return ""
}

// firstForeignTagFromTags returns the first non-quicknote tag from
// Bear's tag array, prepending the leading `#`. Fallback when
// FindForeignTagInBody can't locate a standalone tag-line (rare case
// where the user re-tagged via the bearcli surface or Bear didn't
// insert a body line).
func firstForeignTagFromTags(tags []string) string {
	for _, tag := range tags {
		if topLevelSegment(tag) == "quicknote" {
			continue
		}
		if !strings.HasPrefix(tag, "#") {
			return "#" + tag
		}
		return tag
	}
	return ""
}

// ApplyForeignTagEscape scans every note in the `notes` location, finds
// atoms tagged with both `#quicknote/*` AND a non-quicknote tag, strips
// the `#quicknote/*` lines from each such atom's body, and overwrites
// the note in Bear. The atom exits the quicknote auto-flow forever
// (until the user re-tags it).
//
// Canonical-form bootstrap: when the foreign tag maps to
// a known domain (passed via `domainsByTag`), the stripped body is
// re-rendered in that destination domain's canonical form in the same
// bearcli write — H1 stamped if absent, canonical tag-line + backlink
// emitted directly under H1, user body moved below `---`. The
// subsequent regen cycle for the destination domain no-ops via
// equalIgnoringNewNoteLink. When the foreign tag has no matching
// domain (user-typed ad-hoc tag), the strip happens but no canonical
// render is applied — bearcli still writes the substituted body so the
// `#quicknote/*` token disappears.
//
// Listing reuses the bearcli JSON shape from auto_tag.go (id/title/tags/
// content). Failures per atom are logged and skipped so one bad note
// can't stall the rest of the cycle.
//
// Returns the number of notes actually rewritten (zero when no atom
// carries the foreign-tag mix). Callers use this to decide whether the
// pre-pass produced bearcli writes that need to be self-gated downstream
// (fast-pass gate fix).
func ApplyForeignTagEscape(ctx context.Context, domainsByTag map[string]*Domain) (int, error) {
	out, err := runBearcli(
		ctx,
		[]string{"list", "--location", "notes", flagFormat, formatJSON, flagFields, "id,title,tags,content"},
		"",
	)
	if err != nil {
		return 0, fmt.Errorf("ApplyForeignTagEscape list: %w", err)
	}
	var notes []autoTagNote
	if err = json.Unmarshal(out, &notes); err != nil {
		return 0, fmt.Errorf("ApplyForeignTagEscape parse: %w", err)
	}
	escaped := 0
	for _, note := range notes {
		if err = CheckCtx(ctx); err != nil {
			return escaped, err
		}
		if processForeignTagEscape(ctx, note, domainsByTag) {
			escaped++
		}
	}
	if escaped > 0 {
		log.Printf("foreign-tag escape: %d note(s) released from quicknote", escaped)
	}
	return escaped, nil
}

// processForeignTagEscape handles one note's substitution. Returns true
// when the note was actually rewritten. Logs failures with note context
// so one bad atom doesn't stall the orchestrator loop.
func processForeignTagEscape(ctx context.Context, note autoTagNote, domainsByTag map[string]*Domain) bool {
	if !hasQuicknoteTag(note.Tags) || !HasForeignQuicknoteTag(note.Tags) {
		return false
	}
	foreignTag := FindForeignTagInBody(note.Content)
	if foreignTag == "" {
		foreignTag = firstForeignTagFromTags(note.Tags)
	}
	if foreignTag == "" {
		log.Printf("foreign-tag escape %q: no foreign tag identified, skipping", note.Title)
		return false
	}
	stripped := SubstituteQuicknoteInBody(note.Content, foreignTag)
	if stripped == note.Content {
		return false
	}
	newContent := stripped
	if destDomain := domainsByTag[strings.TrimPrefix(foreignTag, "#")]; destDomain != nil {
		newContent = destDomain.RenderCanonicalForBootstrap(stripped)
	} else {
		log.Printf("foreign-tag escape %q: %s has no registered domain, writing stripped body without canonical bootstrap", note.Title, foreignTag)
	}
	if writeErr := overwriteWithRetry(ctx, note.ID, newContent); writeErr != nil {
		log.Printf("foreign-tag escape %q failed: %v", note.Title, writeErr)
		return false
	}
	log.Printf("foreign-tag escape: %s — %s replaced quicknote tag", note.Title, foreignTag)
	return true
}

// hasQuicknoteTag reports whether `tags` contains at least one tag in
// the quicknote namespace. Used by the escape pre-pass as a fast-path
// fail (atoms not in any quicknote domain are skipped immediately).
// Composes with HasForeignQuicknoteTag, which only fires when both a
// quicknote and a foreign tag are present.
func hasQuicknoteTag(tags []string) bool {
	for _, tag := range tags {
		if topLevelSegment(tag) == "quicknote" {
			return true
		}
	}
	return false
}

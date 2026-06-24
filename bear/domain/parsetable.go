package domain

// Bidirectional-master parsing helpers — the inverse side of the
// renderers. Reads an existing master body (any flavor) and returns
// identifier→bucket maps that drive `computeMasterOverrides` when
// atomics need re-bucketing on the next regen cycle.
//
// Lives in this file (not next to its render-side twin) because
// bear/routing.go, bear/crossmoves.go, bear/snapshot.go, and
// bear/domain.go all call into these functions. Concentrating
// them here keeps the future bear/render/ extraction free of the
// would-be cycle: render emits, this file parses, both halves
// stand on their own.
//
// Sub-tag tag-parsing helpers (`BucketFromSubTag`, `ParseMetaFrom
// SubTag`) live here for the same reason — `bear/routing.go::group
// Atomics` calls `BucketFromSubTag` directly.

import "strings"

// BucketFromSubTag scans Bear's note tags for the first `<top>/<sub>`
// pair matching the domain's family tag and returns `<sub>`.
// Returns "" when no matching sub-tag exists.
//
// Used by groupAtomics as the bucket source for sub-tag-preserving
// blueprints when the canonical-header line is absent — e.g. notes
// freshly created via the Bear sidebar tag-picker, before the
// daemon has had a chance to stamp the `#<top>/<sub> | [[...]]`
// row into the body.
//
// Bear tag-tree is invariant at depth=2 per (family/sub-tag), so we
// require the matched sub-tag to be a single segment with no
// further `/`. No-op for non-sub-tag blueprints: the prefix
// `d.Tag + "/"` never matches a hub-routed or 2-level grouped-vertical domain's
// single-segment family tag.
//
// bearcli `list --tag` and `list --location` both return tags WITH
// a leading `#` prefix; we strip it before prefix-matching so the
// helper is robust regardless of which list call populates
// Note.Tags.
func BucketFromSubTag(d *Domain, tags []string) string {
	prefix := d.Tag + "/"
	for _, tag := range tags {
		sub, ok := strings.CutPrefix(strings.TrimPrefix(tag, "#"), prefix)
		if !ok || sub == "" || strings.Contains(sub, "/") {
			continue
		}
		return sub
	}
	return ""
}

// ParseMetaFromSubTag extracts the bucket from the sub-tag in a
// canonical header line of the form:
//
//	#<top>/<sub> | [[<wikilink>]]
//
// where the wikilink can target either the master (grouped-vertical)
// or a per-bucket hub note (hub-routed-with-subtag). Bucket = `<sub>`.
// Returns an empty AtomicMeta when no `#<top>/<sub>` token is present
// in the header zone — the caller then falls back to
// d.UnknownBucket.
//
// Differs from ParseMetaFlatTable (reads bucket from segment 3) and
// DefaultParseMetaCanonical (reads bucket from the wikilink target).
// Used by every domain whose canonical preserves sub-tags as the
// source of truth.
func ParseMetaFromSubTag(d *Domain, body string) AtomicMeta {
	prefix := d.CanonicalTag + "/"
	for line := range strings.SplitSeq(HeaderZone(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") || !strings.Contains(line, " | ") {
			continue
		}
		parts := DropTrailingNewNoteURLSegment(strings.Split(line, " | "))
		first := strings.TrimSpace(parts[0])
		token := strings.Fields(first)
		if len(token) == 0 {
			continue
		}
		head := token[0]
		if !strings.HasPrefix(head, prefix) {
			continue
		}
		bucket := strings.TrimPrefix(head, prefix)
		if bucket == "" {
			continue
		}
		meta := AtomicMeta{Bucket: bucket}
		if len(parts) >= 3 {
			meta.Section = strings.TrimSpace(parts[2])
		}
		return meta
	}
	return parseBareTagCanonical(d, body)
}

// parseBareTagCanonical is the secondary pass of ParseMetaFromSubTag:
// detects bare `#<tag> ` canonical lines (no `/sub` segment) that the
// primary loop misses. Hub-routed-with-subtag domains see these when
// the user explicitly empties the bucket — `[[]]` returns
// ExplicitlyUncategorized, a real wikilink returns its target as Bucket.
func parseBareTagCanonical(d *Domain, body string) AtomicMeta {
	for line := range strings.SplitSeq(HeaderZone(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, d.CanonicalTag+" ") {
			continue
		}
		parts := DropTrailingNewNoteURLSegment(strings.Split(line, " | "))
		if len(parts) < 2 {
			continue
		}
		target := ExtractWikilinkTarget(parts[1])
		if target == "" {
			return AtomicMeta{ExplicitlyUncategorized: true}
		}
		return AtomicMeta{Bucket: target}
	}
	return AtomicMeta{}
}

// ParseMasterFlatGrouped inverts MasterFlatGrouped — walks an
// existing grouped-vertical master and returns identifier→bucket
// mapping for every atomic referenced under each `##` section.
// Pairs with computeMasterOverrides so users can move atomics
// between buckets by cut/paste in the master.
//
// Identifier semantics match ParseMasterTable: plain `[[Title]]`
// keys by title; `[Label](bear://x-callback-url/open-note?id=X)`
// keys by note ID. Lenient by design — leading blank lines,
// separator rows, hand-curated H3 subsections, and bullets without
// recognizable identifiers are silently skipped without dropping
// the surrounding section.
func ParseMasterFlatGrouped(_ *Domain, masterContent string) map[string]string {
	out := make(map[string]string)
	var currentBucket string
	for line := range strings.SplitSeq(masterContent, "\n") {
		stripped := strings.TrimSpace(line)
		if strings.HasPrefix(stripped, "## ") && !strings.HasPrefix(stripped, "### ") {
			currentBucket = stripHeaderCount(stripped, "## ")
			continue
		}
		if currentBucket == "" {
			continue
		}
		if !strings.HasPrefix(stripped, "-") {
			continue
		}
		for _, ident := range extractCellIdentifiers(stripped) {
			out[ident] = currentBucket
		}
	}
	return out
}

// extractCellIdentifiers pulls every atomic identifier out of a
// single table cell — wikilink targets plus note IDs embedded in
// bear://x-callback URLs. Order: wikilinks first (left-to-right),
// then URL-form IDs.
func extractCellIdentifiers(cell string) []string {
	out := extractCellWikilinks(cell)
	out = append(out, extractCellNoteIDs(cell)...)
	return out
}

// extractCellNoteIDs scans a cell for
// `bear://x-callback-url/open-note?id=<ID>` occurrences and returns
// the IDs. Tolerant of arbitrary surrounding markdown.
func extractCellNoteIDs(cell string) []string {
	var out []string
	const prefix = "bear://x-callback-url/open-note?id="
	rest := cell
	for {
		start := strings.Index(rest, prefix)
		if start < 0 {
			return out
		}
		rest = rest[start+len(prefix):]
		end := strings.IndexAny(rest, ")& \t\n")
		if end < 0 {
			end = len(rest)
		}
		id := rest[:end]
		rest = rest[end:]
		if id != "" {
			out = append(out, id)
		}
	}
}

// extractCellWikilinks pulls every `[[Target]]` (or
// `[[Target|Alias]]`) target out of a single table cell.
func extractCellWikilinks(cell string) []string {
	var out []string
	rest := cell
	for {
		openIdx := strings.Index(rest, "[[")
		if openIdx < 0 {
			return out
		}
		rest = rest[openIdx+2:]
		closeIdx := strings.Index(rest, "]]")
		if closeIdx < 0 {
			return out
		}
		target := rest[:closeIdx]
		rest = rest[closeIdx+2:]
		if pipe := strings.Index(target, "|"); pipe >= 0 {
			target = target[:pipe]
		}
		target = strings.TrimSpace(target)
		if target != "" {
			out = append(out, target)
		}
	}
}

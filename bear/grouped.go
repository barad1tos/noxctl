package bear

import (
	"fmt"
	"sort"
	"strings"
)

// Grouped-vertical master pattern: one master note per domain, body is a
// sequence of `## <bucket> (N)` H2 sections each followed by an alphabetical
// bullet list of the atomics in that bucket. Phone-friendly — vertical scroll,
// no horizontal table overflow. Sub-tags are preserved as the canonical-header
// first token (`#<top>/<bucket>`), so Bear's sidebar still shows the
// 2-level tag tree.
//
// Bidirectional via ParseMasterFlatGrouped: cut a bullet from one `##` section
// and paste under another, save, and the next regen rewrites the matching
// atomic's canonical sub-tag to track the new section.
//
// Suited to medium domains with 3-6 sub-tags where flat-table overflows but a
// per-bucket Tier-2 hub adds no navigation value (english, health, leisure,
// humor).

// BucketFromSubTag scans Bear's note tags for the first `<top>/<sub>` pair
// matching the domain's family tag and returns `<sub>`. Returns "" when no
// matching sub-tag exists.
//
// Used by groupAtomics as the bucket source for sub-tag-preserving blueprints
// when the canonical-header line is absent — e.g. notes freshly created via
// the Bear sidebar tag-picker, before the daemon has had a chance to stamp
// the `#<top>/<sub> | [[...]]` row into the body. Without this sticky-creation
// hook, every fresh user-created note rerouted to d.UnknownBucket and the
// user's explicit sub-tag intent (`#development/ayu-jetbrains`) was lost.
//
// Bear tag-tree is invariant at depth=2 per (family/sub-tag), so we
// require the matched sub-tag to be a single segment with no further `/`.
// No-op for non-sub-tag blueprints: the prefix `d.Tag + "/"` never matches a
// hub-routed or flat-table domain's single-segment family tag.
//
// bearcli `list --tag` and `list --location` both return tags WITH a leading
// `#` prefix (Bear's display form); we strip it before prefix-matching so the
// helper is robust regardless of which list-call populates Note.Tags.
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

// ParseMetaFromSubTag extracts the bucket from the sub-tag in a canonical
// header line of the form:
//
//	#<top>/<sub> | [[<wikilink>]]
//
// where the wikilink can target either the master (grouped-vertical) or a
// per-bucket hub note (hub-routed-with-subtag). Bucket = `<sub>`. Returns an
// empty AtomicMeta when no `#<top>/<sub>` token is present in the header zone
// — the caller then falls back to d.UnknownBucket.
//
// Differs from ParseMetaFlatTable (reads bucket from segment 3) and
// DefaultParseMetaCanonical (reads bucket from the wikilink target). Used by
// every domain whose canonical preserves sub-tags as the source of truth.
func ParseMetaFromSubTag(d *Domain, body string) AtomicMeta {
	prefix := d.CanonicalTag + "/"
	for line := range strings.SplitSeq(HeaderZone(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") || !strings.Contains(line, " | ") {
			continue
		}
		parts := DropTrailingNewNoteURLSegment(strings.Split(line, " | "))
		first := strings.TrimSpace(parts[0])
		// First field may carry extra tags after a space; the canonical
		// sub-tag token is the first whitespace-separated word.
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
	return AtomicMeta{}
}

// RenderMasterFlatGrouped produces the grouped-vertical master body — a
// stack of `## <bucket> (N)` sections each followed by an alphabetical
// bullet list of `[[Title]]` (or bear://x-callback for duplicate titles).
// Buckets are emitted in the order returned by OrderFlatColumns so the
// caller's fixed sequence wins, with overflow buckets appended alphabetically.
//
//	# <IndexTitle>
//	#<tag>
//	---
//	## homework (10)
//	- [[atom1]]
//	- [[atom2]]
//
//	## rules (7)
//	- [[atomA]]
//	-...
func RenderMasterFlatGrouped(d *Domain, groups map[string][]Note, columns []string) string {
	return RenderVerticalSections(d, flatGroupedSections(d, groups, columns))
}

// flatGroupedSections builds the section list for the grouped-vertical
// master from `groups`. Empty buckets are dropped; non-empty buckets emit
// `## <bucket> (N)` with atomics sorted via ByTitle. AtomicWikilink picks
// URL form for duplicate titles automatically.
func flatGroupedSections(d *Domain, groups map[string][]Note, columns []string) []Section {
	cols := OrderFlatColumns(groups, columns)
	sections := make([]Section, 0, len(cols))
	for _, bucket := range cols {
		notes := append([]Note(nil), groups[bucket]...)
		if len(notes) == 0 {
			continue
		}
		sort.Sort(ByTitle(notes))
		bullets := make([]string, len(notes))
		for index, note := range notes {
			bullets[index] = AtomicWikilink(d, note)
		}
		sections = append(sections, Section{
			Header:  fmt.Sprintf("%s (%d)", bucket, len(notes)),
			Bullets: bullets,
		})
	}
	return sections
}

// ParseMasterFlatGrouped inverts RenderMasterFlatGrouped — walks an existing
// grouped-vertical master and returns identifier→bucket mapping for every
// atomic referenced under each `##` section. Pairs with computeMasterOverrides
// so users can move atomics between buckets by cut/paste in the master.
//
// Identifier semantics match ParseMasterTable: plain `[[Title]]` keys by
// title; `[Label](bear://x-callback-url/open-note?id=X)` keys by note ID.
// Lenient by design — leading blank lines, separator rows, hand-curated H3
// subsections, and bullets without recognizable identifiers are silently
// skipped without dropping the surrounding section.
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

// NewGroupedVerticalDomain builds a Domain configured for the grouped-vertical
// master pattern with sub-tag preservation. Atomics carry canonical headers
// of the form `#<tag>/<bucket> | [[<indexTitle>]]`; the master renders one
// `## <bucket> (N)` section per bucket; bidirectional via the master.
//
// Parameters: `tag` and `indexTitle` identify the domain; `unknownBucket`
// catches atomics whose canonical lacks a sub-tag (a bare `#<tag>` token
// without `/...`) — they surface in a final `## <unknownBucket>` section
// and the user can re-bucket via cut/paste like any other entry.
// `buckets` defines the priority left-to-right column order; new buckets
// from atomic canonicals append alphabetically after the priority list.
func NewGroupedVerticalDomain(tag, indexTitle, unknownBucket string, buckets []string) *Domain {
	columns := append([]string(nil), buckets...)
	return &Domain{
		Tag:              tag,
		CanonicalTag:     "#" + tag,
		IndexTitle:       indexTitle,
		UnknownBucket:    unknownBucket,
		HubH2Prefix:      "",
		ParseMeta:        ParseMetaFromSubTag,
		BacklinkFor:      MasterBacklink,
		SectionFor:       BucketAsSection,
		RenderHub:        nil,
		ParseMasterTable: ParseMasterFlatGrouped,
		CanonicalTagFor:  SubTagCanonical,
		RenderMaster: func(d *Domain, groups map[string][]Note) string {
			return RenderMasterFlatGrouped(d, groups, columns)
		},
	}
}

// SubTagCanonical is a Domain.CanonicalTagFor implementation that emits
// `#<top>/<bucket>` per atomic. Falls back to d.CanonicalTag when bucket is
// empty (atomic has no recognizable sub-tag — daemon writes the bare top-level
// tag and the user picks a bucket later via the master).
func SubTagCanonical(d *Domain, bucket string) string {
	if bucket == "" {
		return d.CanonicalTag
	}
	return d.CanonicalTag + "/" + bucket
}

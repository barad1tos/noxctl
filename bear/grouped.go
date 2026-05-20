package bear

import (
	"fmt"
	"sort"
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

// RenderMasterFlatGrouped produces the grouped-vertical master body —
// a stack of `## <bucket> (N)` sections each followed by an
// alphabetical bullet list of `[[Title]]` (or
// `bear://x-callback-url/open-note?id=X` for duplicate titles).
// Buckets are emitted in the order returned by OrderFlatColumns so
// the caller's fixed sequence wins, with overflow buckets appended
// alphabetically.
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

// NewGroupedVerticalDomain builds a Domain configured for the
// grouped-vertical master pattern with sub-tag preservation. Atomics
// carry canonical headers of the form `#<tag>/<bucket> | [[<indexTitle>]]`;
// the master renders one `## <bucket> (N)` section per bucket;
// bidirectional via the master.
//
// Parameters: `tag` and `indexTitle` identify the domain;
// `unknownBucket` catches atomics whose canonical lacks a sub-tag
// (a bare `#<tag>` token without `/...`) — they surface in a final
// `## <unknownBucket>` section and the user can re-bucket via
// cut/paste like any other entry. `buckets` defines the priority
// left-to-right column order; new buckets from atomic canonicals
// append alphabetically after the priority list.
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

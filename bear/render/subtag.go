package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/barad1tos/noxctl/bear/domain"
)

// Hub-routed-with-subtag pattern: like the existing hub-routed shape used by
// llm/agents but with sub-tags preserved as the source of truth. Atomic
// canonical headers are `#<top>/<bucket> | [[<top> · <bucket>]]`. Each
// `<top> · <bucket>` is a Tier-2 hub note that carries `#<top>/<bucket>` itself
// — Bear's sidebar still shows the 2-level tag tree the user expects.
//
// Master is a phone-friendly bullet list of hub wikilinks under
// `## Категорії (N)`; each hub note's body is a vertical bullet list of its
// atomics. No wide tables anywhere. Suited to large domains where a single
// grouped-vertical master note would be too long (e.g. claude with 60+
// atomics across 7 categories).
//
// Hub naming uses U+00B7 MIDDLE DOT (`·`) padded with spaces — `claude · sessions`.
// The space-dot-space form keeps wikilink targets unambiguous and survives
// Bear's title sanitization.

// HubTitleSeparator is the visual separator between top-tag and bucket in
// hub note titles produced by HubTitleSubTag (`<top> · <bucket>`).
const HubTitleSeparator = " · "

// HubTitleSubTag is a domain.Domain.HubTitleFor implementation that namespaces
// Tier-2 hub notes as `<top> · <bucket>`. Prevents collisions with arbitrary
// user notes that happen to share a bucket name (e.g. a personal note titled
// `sessions` would otherwise be picked up by daemon's `findHubID("sessions")`
// for a `claude · sessions` hub).
func HubTitleSubTag(d *domain.Domain, bucket string) string {
	return d.Tag + HubTitleSeparator + bucket
}

// BucketFromHubTitleSubTag inverts HubTitleSubTag — strips the `<top> · `
// prefix and returns the bucket. Returns "" when the title is not one of
// our hubs (signals computeHubOverrides to skip this note).
func BucketFromHubTitleSubTag(d *domain.Domain, title string) string {
	prefix := d.Tag + HubTitleSeparator
	if !strings.HasPrefix(title, prefix) {
		return ""
	}
	return strings.TrimPrefix(title, prefix)
}

// IsHubNoteSubTag reports whether `n` is a Tier-2 hub for this sub-tag-
// preserving domain — its title matches the `<top> · <bucket>` pattern. The
// content is unused; title alone is sufficient because daemon owns the
// title space (BearcliFindByTitle for the master, hub titles for hubs).
func IsHubNoteSubTag(d *domain.Domain, n domain.Note) bool {
	prefix := d.Tag + HubTitleSeparator
	return strings.HasPrefix(n.Title, prefix)
}

// HubBacklinkSubTag is a domain.Domain.BacklinkFor implementation that backlinks
// each atomic at its per-bucket Tier-2 hub note (`[[<top> · <bucket>]]`),
// not at the master. Pairs with the hub-side bidirectional flow: a bullet
// inside a hub claims its atomic for that hub's bucket.
func HubBacklinkSubTag(d *domain.Domain, bucket string) string {
	return "[[" + d.Tag + HubTitleSeparator + bucket + "]]"
}

// HubFlatSubTag produces a Tier-2 hub note's auto-zone for the
// sub-tag preserving hub-routed pattern. Hub body is a flat alphabetical
// bullet list of its atomics — no internal H2 sections, no `## HubH2Prefix`
// header (that lives in the master). Phone-friendly.
//
//	# <top> · <bucket>
//	#<top>/<bucket> | [[<IndexTitle>]]
//	---
//	- [[atom1]]
//	- [[atom2]]
func HubFlatSubTag(d *domain.Domain, bucket string, notes []domain.Note, _ map[string][]string) string {
	hubTitle := d.HubTitle(bucket)
	canonicalTag := d.ResolveCanonicalTag(bucket)
	sorted := append([]domain.Note(nil), notes...)
	sort.Sort(domain.ByTitle(sorted))

	var body strings.Builder
	_, _ = fmt.Fprintf(&body, "# %s\n", hubTitle)
	_, _ = fmt.Fprintf(&body, "%s | [[%s]]%s\n---\n",
		canonicalTag, d.IndexTitle, domain.NewNoteURLFromDomain(d).Emit())
	for _, note := range sorted {
		_, _ = fmt.Fprintf(&body, "- %s\n", domain.AtomicWikilink(d, note))
	}
	return body.String()
}

// MasterHubList produces the sub-tag preserving master body — a
// `## Категорії (N)` heading followed by a wikilink-per-line list of every
// hub bucket with its count. No atomic-level bullets at master level; the
// user clicks into a hub to see atomics.
//
//	# <IndexTitle>
//	#<tag>
//	---
//	## Категорії (7)
//	- [[<top> · sessions]] (15)
//	- [[<top> · memory]] (18)
//	-...
func MasterHubList(d *domain.Domain, groups map[string][]domain.Note, columns []string) string {
	ordered := OrderFlatColumns(groups, columns)
	nonEmpty := make([]string, 0, len(ordered))
	total := 0
	for _, bucket := range ordered {
		if len(groups[bucket]) == 0 {
			continue
		}
		nonEmpty = append(nonEmpty, bucket)
		total += len(groups[bucket])
	}
	bullets := make([]string, len(nonEmpty))
	for index, bucket := range nonEmpty {
		bullets[index] = fmt.Sprintf("[[%s]] (%d)", d.HubTitle(bucket), len(groups[bucket]))
	}
	return VerticalSections(d, []Section{{
		Header:  fmt.Sprintf("%s (%d)", domain.T("master.section.categories"), total),
		Bullets: bullets,
	}})
}

// NewHubRoutedSubTagDomain builds a domain.Domain configured for the sub-tag
// preserving hub-routed pattern. Each bucket gets a Tier-2 hub note
// `<top> · <bucket>` with a flat bullet list of its atomics; the master
// indexes those hubs via `## Категорії`. Atomics carry canonical headers
// `#<top>/<bucket> | [[<top> · <bucket>]]` so Bear's tag tree retains the
// 2-level structure and bidirectional via hub-side cut/paste works.
//
// Parameters mirror NewGroupedVerticalDomain. `unknownBucket` catches atomics
// without a recognizable sub-tag — they go into a hub `<top> · <unknownBucket>`
// like any other bucket; user can re-bucket via cut/paste in any of the
// per-bucket hubs.
func NewHubRoutedSubTagDomain(tag, indexTitle, unknownBucket string, buckets []string) *domain.Domain {
	columns := append([]string(nil), buckets...)
	return &domain.Domain{
		Tag:                tag,
		CanonicalTag:       "#" + tag,
		IndexTitle:         indexTitle,
		UnknownBucket:      unknownBucket,
		Buckets:            columns,
		HubH2Prefix:        "",
		ParseMeta:          domain.ParseMetaFromSubTag,
		BacklinkFor:        HubBacklinkSubTag,
		SectionFor:         BucketAsSection,
		CanonicalTagFor:    SubTagCanonical,
		IsHubNote:          IsHubNoteSubTag,
		HubTitleFor:        HubTitleSubTag,
		BucketFromHubTitle: BucketFromHubTitleSubTag,
		RenderHub:          HubFlatSubTag,
		RenderMaster: func(d *domain.Domain, groups map[string][]domain.Note) string {
			return MasterHubList(d, groups, columns)
		},
	}
}

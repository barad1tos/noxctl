package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/barad1tos/noxctl/bear/domain"
)

// DefaultParseMetaCanonical extracts (Bucket, Section) from a canonical-shape
// header line of the form:
//
//	#<tag> [extras] | [[Bucket]] [| section]
//
// Skips when the wikilink target equals d.IndexTitle (defensive against
// malformed lines that mistakenly point at the master). Maps OwnAliases to
// d.OwnGroup. The third pipe-segment, when present, becomes Section.
//
// Use this for any 3-tier domain whose canonical header backlinks at the
// per-bucket Hub note (poetry, articles, future library/lyrics, …). Domains
// where the canonical header backlinks at the MASTER (aphorisms-style — the
// "category-as-bucket" pattern) need their own ParseMeta because they require
// target == IndexTitle rather than rejecting it.
func DefaultParseMetaCanonical(d *domain.Domain, body string) domain.AtomicMeta {
	for line := range strings.SplitSeq(domain.HeaderZone(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") || !strings.Contains(line, "[[") {
			continue
		}
		parts := domain.DropTrailingNewNoteURLSegment(strings.Split(line, " | "))
		if len(parts) < 2 {
			continue
		}
		target := domain.ExtractWikilinkTarget(parts[1])
		if target == "" {
			return domain.AtomicMeta{ExplicitlyUncategorized: true}
		}
		if target == d.IndexTitle {
			continue
		}
		var meta domain.AtomicMeta
		if _, isOwnAlias := d.OwnAliases[target]; isOwnAlias {
			meta.Bucket = d.OwnGroup
		} else {
			meta.Bucket = target
		}
		if len(parts) >= 3 {
			meta.Section = strings.TrimSpace(parts[2])
		}
		return meta
	}
	return domain.AtomicMeta{}
}

// DefaultRenderHub3Tier produces a per-bucket Hub note's auto-zone for any
// 3-tier domain. Header H2 uses d.HubH2Prefix (e.g. "Поеми" / "Статті"). When
// existingOrder is non-nil the caller's prior bullet sequence is preserved
// per-section; new entries append at section bottom.
//
//	# <Bucket>
//	#<tag> | [[<IndexTitle>]]
//	---
//	## <HubH2Prefix> (N)
//	- [[unsectioned item]]
//
// ...
//
//	### <subsection> (M)
//	- [[item]]
//	#### <nested> (K)
//	- [[item]]
func DefaultRenderHub3Tier(
	d *domain.Domain, name string, notes []domain.Note,
	existingOrder map[string][]string,
) string {
	sorted := make([]domain.Note, len(notes))
	copy(sorted, notes)
	sort.Sort(domain.ByTitle(sorted))

	bySection := d.GroupNotesBySection(sorted)
	if existingOrder != nil {
		domain.ApplyOrder(bySection, existingOrder)
	}
	topKeys, topGroups := domain.NestSections(bySection)

	var body strings.Builder
	_, _ = fmt.Fprintf(&body, "# %s\n", name)
	_, _ = fmt.Fprintf(&body, "%s | [[%s]]%s\n---\n",
		d.CanonicalTag, d.IndexTitle, domain.NewNoteURLFromDomain(d).Emit())
	_, _ = fmt.Fprintf(&body, "## %s (%d)\n", d.HubH2Prefix, len(sorted))
	domain.RenderNoteList(&body, d, bySection[""])
	for _, top := range topKeys {
		domain.RenderSectionGroup(&body, d, top, topGroups[top])
	}
	body.WriteString("\n")
	return body.String()
}

// DefaultRenderMaster3Tier produces the standard master index for a 3-tier
// domain: ## Власні (own group, when present) followed by ## Автори listing
// every other bucket alphabetically with its count. Mirrors the layout of
// `✱ Поезія` and is reused by all author-grouped domains.
//
//	# <IndexTitle>
//	#<tag>
//	---
//	## Власні
//	- [[<OwnGroup>]] (N)
//
//	## Автори (M)
//	- [[Bucket]] (K)
//
// ...
func DefaultRenderMaster3Tier(d *domain.Domain, groups map[string][]domain.Note) string {
	return VerticalSections(d, defaultMaster3TierSections(d, groups))
}

// defaultMaster3TierSections builds the section list for poetry/articles-
// style masters: an optional "Власні" section for the OwnGroup bucket,
// then "Автори (M)" listing the remaining buckets alphabetically with
// per-author counts in the bullet text.
func defaultMaster3TierSections(d *domain.Domain, groups map[string][]domain.Note) []Section {
	var sections []Section
	if d.OwnGroup != "" {
		if own, hasOwn := groups[d.OwnGroup]; hasOwn {
			sections = append(sections, Section{
				Header:  domain.T("master.section.own-curated"),
				Bullets: []string{fmt.Sprintf("[[%s]] (%d)", d.OwnGroup, len(own))},
			})
		}
	}
	total := 0
	authors := make([]string, 0, len(groups))
	for author := range groups {
		if author == d.OwnGroup {
			continue
		}
		authors = append(authors, author)
		total += len(groups[author])
	}
	SortTitles(authors)
	bullets := make([]string, len(authors))
	for index, author := range authors {
		bullets[index] = fmt.Sprintf("[[%s]] (%d)", author, len(groups[author]))
	}
	sections = append(sections, Section{
		Header:  fmt.Sprintf("%s (%d)", domain.T("master.section.authors"), total),
		Bullets: bullets,
	})
	return sections
}

// DefaultRenderMasterFlat renders the master as a single alphabetical list of
// every atomic, ignoring bucket boundaries. Suited to small flat-list domains
// (≤20 atomics, no meaningful sub-categorisation) where per-bucket hubs add
// no navigation value — characters, rules, tips. The master itself becomes
// the only navigation surface; clicking a bullet opens the atomic directly.
//
//	# <IndexTitle>
//	#<tag>
//	---
//	- [[A]]
//	- [[B]]
//
// ...
func DefaultRenderMasterFlat(d *domain.Domain, groups map[string][]domain.Note) string {
	var all []domain.Note
	for _, items := range groups {
		all = append(all, items...)
	}
	sort.Sort(domain.ByTitle(all))
	bullets := make([]string, len(all))
	for index, note := range all {
		bullets[index] = domain.AtomicWikilink(d, note)
	}
	return VerticalSections(d, []Section{{Bullets: bullets}})
}

// ParseMetaFlatTable extracts the bucket from a strict canonical header
// where the wikilink target points at the master itself (NOT a per-bucket
// Tier-2 hub) and the 3rd pipe-segment holds the bucket name. The shape is:
//
//	#<tag> | [[<IndexTitle>]] | <bucket>
//
// Used by every 2-level grouped-vertical domain (aphorisms, prose, it/vendors,
// it/technologies). Differs from DefaultParseMetaCanonical, which expects
// target == bucket name (Tier-2 hub backlink) and treats the 3rd segment
// as a section path inside that hub.
func ParseMetaFlatTable(d *domain.Domain, body string) domain.AtomicMeta {
	for line := range strings.SplitSeq(domain.HeaderZone(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") || !strings.Contains(line, "[[") {
			continue
		}
		parts := domain.DropTrailingNewNoteURLSegment(strings.Split(line, " | "))
		if len(parts) < 3 {
			continue
		}
		target := domain.ExtractWikilinkTarget(parts[1])
		if target != d.IndexTitle {
			continue
		}
		bucket := strings.TrimSpace(parts[2])
		if bucket == "[[]]" {
			return domain.AtomicMeta{ExplicitlyUncategorized: true}
		}
		if bucket == "" {
			continue
		}
		return domain.AtomicMeta{Bucket: bucket}
	}
	return domain.AtomicMeta{}
}

// WriteMasterHeader emits the standard master-note header into the supplied
// builder — H1 with the IndexTitle, the canonical tag-line, then the `---`
// separator. Every master renderer starts with this exact prelude, so
// centralizing the template removes 8 copies of the same fmt.Fprintf format
// string across the codebase.
//
//	# <IndexTitle>
//	#<tag>
//	---
func WriteMasterHeader(b *strings.Builder, d *domain.Domain) {
	link := domain.NewNoteURLFromDomain(d).Emit()
	if d.ParentMaster != "" {
		_, _ = fmt.Fprintf(b, "# %s\n%s | [[%s]]%s\n---\n",
			d.IndexTitle, d.CanonicalTag, d.ParentMaster, link)
		return
	}
	_, _ = fmt.Fprintf(b, "# %s\n%s%s\n---\n", d.IndexTitle, d.CanonicalTag, link)
}

// SortTitles sorts the supplied slice in place using domain.CompareTitles —
// Latin → Ukrainian → Russian-only → other, lowercase tie-break. The
// idiom `sort.Slice(s, func(i,j int) bool { return domain.CompareTitles(s[i], s[j]) < 0 })`
// recurs in every renderer; centralizing it keeps duplicate-block lints
// quiet without inventing a custom sort.Interface per call site.
func SortTitles(items []string) {
	sort.Slice(items, func(i, j int) bool {
		return domain.CompareTitles(items[i], items[j]) < 0
	})
}

// MasterBacklink is a domain.Domain.BacklinkFor implementation that points every
// atomic at the master `[[<IndexTitle>]]`, regardless of bucket. Use for
// 2-level grouped-vertical domains (aphorisms, prose, llm/characters, llm/rules,
// llm/tips) where atomics have no per-bucket Tier-2 hub to backlink at.
//
// Round-trip stability: DefaultParseMetaCanonical drops lines whose target
// equals IndexTitle, so the bucket empties and falls back to
// domain.Domain.UnknownBucket — predictable across regen cycles.
func MasterBacklink(d *domain.Domain, _ string) string {
	return "[[" + d.IndexTitle + "]]"
}

// BucketAsSection is a domain.Domain.SectionFor implementation that writes the
// bucket name back into the canonical header's third pipe-segment. Pairs
// with custom ParseMeta callbacks that read bucket from segment 3 (e.g.
// aphorisms, prose) — bucket==section by construction, so the canonical
// form round-trips without daemon-induced drift.
func BucketAsSection(_ *domain.Domain, bucket string, _ domain.AtomicParts) string {
	return bucket
}

// OrderFlatColumns returns the column sequence for a grouped-vertical master:
// the supplied `fixedOrder` first (always rendered, even when empty so
// the user sees the slot), then any bucket present in `groups` that
// isn't in fixedOrder, alphabetised via domain.CompareTitles. Used by every
// grouped-vertical renderer that wants a deterministic priority layout with
// a graceful overflow column for new buckets.
func OrderFlatColumns(groups map[string][]domain.Note, fixedOrder []string) []string {
	fixed := make(map[string]struct{}, len(fixedOrder))
	for _, bucket := range fixedOrder {
		fixed[bucket] = struct{}{}
	}
	extras := make([]string, 0)
	for bucket := range groups {
		if _, isFixed := fixed[bucket]; isFixed {
			continue
		}
		extras = append(extras, bucket)
	}
	SortTitles(extras)
	out := make([]string, 0, len(fixedOrder)+len(extras))
	out = append(out, fixedOrder...)
	out = append(out, extras...)
	return out
}

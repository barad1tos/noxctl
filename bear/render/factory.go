// Package render builds Domain instances and emits master / hub / atom
// note bodies. Pure rendering layer — depends on bear/domain for the
// Domain type and on bear/note for the Note value type; no inverse
// dependency. Factory functions wire blueprint-specific render and
// parse callbacks into Domain struct fields.
package render

import (
	"fmt"
	"strings"

	"github.com/barad1tos/noxctl/bear/domain"
)

// Factory functions for the recurring domain.Domain shapes. They keep the per-domain
// config files down to the parameters that genuinely vary (tag, master title,
// bucket data) without duplicating the boilerplate that's identical across
// every flat-table or flat-list domain. Driven by `golangci-lint dupl` finding
// it/vendors.go ↔ it/technologies.go and llm/{characters,rules,tips}.go as
// 50+ token templates — the duplication was real, the right answer was a
// factory, not a higher threshold.

// NewGroupedVerticalFlatDomain produces a domain.Domain whose atomics carry the
// `#<tag> | [[<indexTitle>]] | <bucket>` canonical (no sub-tag preserved
// at the Bear sidebar level — Bear shows only `#<tag>`) and whose master
// renders as a stack of `## <bucket> (N)` H2 sections with bullet lists.
// Suited to small/medium domains where atom buckets are user-curated values
// not promoted into Bear's tag tree (it/vendors, it/technologies,
// library/aphorisms, library/prose).
//
// Bidirectional via domain.ParseMasterFlatGrouped: cut a bullet from one section,
// paste under another, save, and the next regen rewrites the atomic's
// canonical 3rd segment to track the new section.
func NewGroupedVerticalFlatDomain(tag, indexTitle, unknownBucket string, buckets []string) *domain.Domain {
	columns := append([]string(nil), buckets...)
	return &domain.Domain{
		Tag:              tag,
		CanonicalTag:     "#" + tag,
		IndexTitle:       indexTitle,
		UnknownBucket:    unknownBucket,
		HubH2Prefix:      "",
		ParseMeta:        ParseMetaFlatTable,
		BacklinkFor:      MasterBacklink,
		SectionFor:       BucketAsSection,
		RenderHub:        nil,
		ParseMasterTable: domain.ParseMasterFlatGrouped,
		RenderMaster: func(d *domain.Domain, groups map[string][]domain.Note) string {
			return MasterFlatGrouped(d, groups, columns)
		},
	}
}

// NewUmbrellaDomain produces a domain.Domain that emits a top-level-tag directory
// master listing each child sub-domain's master with a live atom count:
//
//	# ✱ IT
//	#it
//	---
//
//	## Розділи (3)
//	- [[✱ IT Сфери]] (1)
//	- [[✱ IT Vendors]] (8)
//	- [[✱ IT Технології]] (16)
//
// Reuses the standard RunRegen pipeline (listNotes → groupAtomics → master
// upsert) with SkipAtomicsPass=true so the canonicalizer doesn't clobber
// child-domain atom canonicals. domain.ParseMetaFromSubTag groups atomics by their
// sub-tag, so `groups["vendors"]` is the live atom count for `#it/vendors`.
// SkipNote delegates to each child's predicate so Tier-2 hubs of children
// (e.g. `python` hub of llm/agents) don't inflate the umbrella's counts.
// Children with zero atomics still render so the user sees the full
// directory.
//
// `defaultChild` must match the Tag of one of the (non-umbrella) children —
// it's the leaf the umbrella master's "Нова нотатка" link targets so clicks
// land in a tagged leaf domain instead of the bare umbrella tag. Production
// callers panic on misconfiguration; the soft-error variant is
// newUmbrellaDomainStrict (used by the TOML loader + test helper).
func NewUmbrellaDomain(tag, indexTitle, defaultChild string, children []*domain.Domain) *domain.Domain {
	d, err := newUmbrellaDomainStrict(tag, indexTitle, defaultChild, children)
	if err != nil {
		panic(fmt.Sprintf("NewUmbrellaDomain(%q): %v", tag, err))
	}
	return d
}

// newUmbrellaDomainStrict assembles an umbrella domain.Domain after enforcing
// the cross-domain DefaultChild rules: must be non-empty, must match a
// registered child Tag, must not itself be an umbrella. Returns the
// assembled domain.Domain or an error so the TOML loader can surface a clean
// error path instead of crashing on malformed config.
func newUmbrellaDomainStrict(tag, indexTitle, defaultChild string, children []*domain.Domain) (*domain.Domain, error) {
	if defaultChild == "" {
		return nil, fmt.Errorf("DefaultChild is required for umbrella %q", tag)
	}
	var matched *domain.Domain
	for _, c := range children {
		if c.Tag == defaultChild {
			matched = c
			break
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("DefaultChild %q does not match any registered child of umbrella %q", defaultChild, tag)
	}
	if matched.SkipAtomicsPass {
		return nil, fmt.Errorf(
			"DefaultChild %q points at a nested umbrella "+
				"(umbrellas of umbrellas not allowed) for umbrella %q",
			defaultChild, tag,
		)
	}
	frozen := append([]*domain.Domain(nil), children...)
	// Wire the upward parent-master backlink on each child so its master's
	// tag-line carries `#<child-tag> | [[<umbrella-master>]]`. Side-effect on
	// the (long-lived global) child domain.Domain pointers is intentional — done
	// once at init, before any RunRegen reads d.ParentMaster.
	for _, child := range frozen {
		child.ParentMaster = indexTitle
	}
	return &domain.Domain{
		Tag:                tag,
		CanonicalTag:       "#" + tag,
		IndexTitle:         indexTitle,
		UnknownBucket:      "_umbrella",
		DefaultChild:       defaultChild,
		DefaultChildDomain: matched,
		ParseMeta:          domain.ParseMetaFromSubTag,
		SkipAtomicsPass:    true,
		SkipNote:           umbrellaSkipNote(frozen),
		RenderMaster:       umbrellaRenderMaster(frozen),
	}, nil
}

// NewUmbrellaDomainForTest is the test-only wrapper that surfaces factory
// validation as a Validate error rather than a panic, so table-driven
// tests can assert rejection without crashing the test binary. Production
// code uses NewUmbrellaDomain directly.
//
// The `t` parameter is constrained to the `Helper` method only — keeps
// the bear package free of the `testing` import while letting tests pass a
// *testing.T at the call site.
func NewUmbrellaDomainForTest(
	t interface{ Helper() },
	tag, indexTitle, defaultChild string,
	children []*domain.Domain,
) *domain.Domain {
	t.Helper()
	d, err := newUmbrellaDomainStrict(tag, indexTitle, defaultChild, children)
	if err != nil {
		return &domain.Domain{
			Tag:             tag,
			CanonicalTag:    "#" + tag,
			IndexTitle:      indexTitle,
			SkipAtomicsPass: true,
			DefaultChild:    defaultChild,
			ParseMeta:       domain.ParseMetaFromSubTag,
			RenderMaster:    umbrellaRenderMaster(children),
			ValidationError: err,
		}
	}
	return d
}

// umbrellaSkipNote returns a SkipNote callback that drops the umbrella's own
// master, decorative ✱-prefixed notes, and any note that any child domain's
// own skipNote also drops (catching child masters and Tier-2 hubs of the
// children — e.g. `python` hub of llm/agents).
func umbrellaSkipNote(children []*domain.Domain) func(d *domain.Domain, n domain.Note) bool {
	return func(d *domain.Domain, n domain.Note) bool {
		if n.Title == d.IndexTitle {
			return true
		}
		if strings.HasPrefix(n.Title, "[Index]") || strings.HasPrefix(n.Title, "✱ ") {
			return true
		}
		for _, child := range children {
			if domain.IsAuxNote(child, n) {
				return true
			}
		}
		return false
	}
}

// umbrellaRenderMaster returns the closure that emits the umbrella master
// from the live group counts. Children render in declared order; bucket key
// is the last path segment of the child tag (`it/vendors` → `vendors`).
func umbrellaRenderMaster(children []*domain.Domain) func(d *domain.Domain, groups map[string][]domain.Note) string {
	return func(d *domain.Domain, groups map[string][]domain.Note) string {
		bullets := make([]string, len(children))
		for index, child := range children {
			bullets[index] = fmt.Sprintf("[[%s]] (%d)",
				child.IndexTitle, len(groups[child.TagSuffix()]))
		}
		return VerticalSections(d, []Section{{
			Header:  fmt.Sprintf("%s (%d)", domain.T("master.section.divisions"), len(children)),
			Bullets: bullets,
		}})
	}
}

// NewFlatListDomain produces a domain.Domain that renders its master as a single
// alphabetical bullet list with no Tier-2 hubs and no sub-grouping. Suited
// to small low-cardinality corpora where a per-bucket hub layer adds no
// navigation value. Used by llm/characters, llm/rules, llm/tips, it/domains
// (placeholder), and any future domain matching this shape.
//
// All atomics fall into a single internal bucket keyed `_flat`; the key is
// never user-facing (master renders one flat list regardless).
func NewFlatListDomain(tag, indexTitle string) *domain.Domain {
	return &domain.Domain{
		Tag:           tag,
		CanonicalTag:  "#" + tag,
		IndexTitle:    indexTitle,
		UnknownBucket: "_flat",
		HubH2Prefix:   "",
		ParseMeta:     DefaultParseMetaCanonical,
		BacklinkFor:   MasterBacklink,
		RenderHub:     nil,
		RenderMaster:  DefaultRenderMasterFlat,
	}
}

// NewHubRoutedDomain produces a domain.Domain configured for the standard 3-tier
// hub-routed pattern: atomic → per-bucket Tier-2 hub → master. Atomics
// backlink at their hub (`[[<Bucket>]]`), the master indexes the hubs.
// Used by lyrics, quotes, and any future domain matching this shape.
//
// LegacyAuthorFallback is enabled by default (poetry/articles convention —
// pre-canonical atomics carry `## <Bucket>` H2 in body; the canonicalizer
// promotes the H2 into the header on first regen and strips it). Domains
// that should NOT do that (e.g. atomics whose body H2s are content
// sections) build the domain.Domain literal directly instead of using this factory.
//
// Caller supplies the master renderer because masters tend to be the most
// domain-specific part — lyrics groups by alphabet, quotes by source label,
// poetry uses the default `## Власні / ## Автори` layout.
func NewHubRoutedDomain(
	tag, indexTitle, unknownBucket, hubH2Prefix string,
	renderMaster func(d *domain.Domain, groups map[string][]domain.Note) string,
) *domain.Domain {
	return &domain.Domain{
		Tag:                  tag,
		CanonicalTag:         "#" + tag,
		IndexTitle:           indexTitle,
		UnknownBucket:        unknownBucket,
		HubH2Prefix:          hubH2Prefix,
		LegacyAuthorFallback: true,
		StripLegacyAuthorH2:  true,
		ParseMeta:            DefaultParseMetaCanonical,
		RenderHub:            DefaultRenderHub3Tier,
		RenderMaster:         renderMaster,
	}
}

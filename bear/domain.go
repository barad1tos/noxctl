// Package bear is the Bear-specific framework that powers the regen-watchd
// daemon: bearcli wrappers, canonical-header parsing, sort comparators, and
// the *Domain abstraction with its core algorithms.
//
// Per-tag configurations live in the sibling `modules/` package — adding a
// new tag is one new file there with a `var FooDomain = &bear.Domain{...}`
// literal, plus one append to main's `domains` slice.
package bear

import "github.com/barad1tos/noxctl/bear/note"

// ErrHashConflict + BearcliTimeout + bearcli command-line constants
// (FlagFormat, FlagFields, FlagBase, FormatJSON, FieldsIDTitle,
// FieldsAutoTag) now live in bear/bearcli. bear/ re-exports the
// most-used ones via bear/aliases.go for backward
// compatibility.

// HubMarker splits a Hub note's auto-zone (above) from the
// curator-managed zone (below). Same convention is reused across all
// domains' hubs and master notes that may carry hand-written
// commentary.
//
//cyrillic:permit
const HubMarker = "## ✱ Куратор"

// Cyrillic alphabet ordering for the ByAuthor sort comparator. Latin first,
// then Ukrainian, then Russian-only, then anything else.
//
// These two strings are alphabet tables, not user-facing copy — bear.T
// would not be a meaningful indirection.
const (
	//cyrillic:permit
	uaOrder = "АБВГҐДЕЄЖЗИІЇЙКЛМНОПРСТУФХЦЧШЩЬЮЯ"
	//cyrillic:permit
	ruOrder = "АБВГДЕЁЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯ"
)

// Note is re-exported here as a type alias so existing call sites
// (bear.Note across 30+ files) keep compiling unchanged. The canonical
// definition lives in bear/note/note.go — a leaf package every layer
// (bearcli, render, fastpass, audit) imports without forming a cycle
// back through bear/.
//
// New code should prefer the bear/note import; this alias exists for
// migration compatibility and may be removed once every caller has
// been moved over.
type Note = note.Note

// AtomicMeta is the structured form of an atomic note's canonical header line.
// Semantics of `bucket` and `section` differ per domain; see ParseMeta callback.
type AtomicMeta struct {
	Bucket  string
	Section string
}

// AtomicParts holds the destructured form of an atomic note's content during
// parseAtomicContent / renderAtomicCanonical round-trip.
//
// PreambleLines captures non-tag-line content that lives BETWEEN the H1
// and the first canonical header line. Per spec component 5 those
// lines must round-trip in-place at render time (between H1 and the
// canonical tag-line) — not get pushed below `---` into the body zone.
type AtomicParts struct {
	H1Line        string
	PreambleLines []string
	ExtraTags     []string
	Section       string
	BodyLines     []string
}

// Domain encapsulates everything that varies between library/* tag domains.
// Adding a new domain = one new Domain literal + register in main.go.
type Domain struct {
	// Identity
	Tag          string // "library/poetry"      bearcli --tag value
	CanonicalTag string // "#" + Tag             header-line first token
	IndexTitle   string // "✱ Поезія" / "03-1-афоризми"

	// Bucketing
	OwnGroup      string              // "" disables; "Моя поезія" for poetry
	OwnAliases    map[string]struct{} // legacy markers folding into OwnGroup
	UnknownBucket string              // fallback: "Невідомі" / "Книги"

	// Tier-2 Hub semantics
	HubH2Prefix string   // "Поеми"; "" disables Tier-2 entirely
	HubH2Legacy []string // ["Поезії"] for one-off transition

	// Behavior toggles
	// poetry=true (firstNonSectionH2 fallback); aphorisms=false (quote H2s would be misread).
	LegacyAuthorFallback bool
	// poetry=true (parseAtomicContent strips "## <author>"); aphorisms=false (preserve content H2s).
	StripLegacyAuthorH2 bool

	// Pluggable callbacks (REQUIRED unless explicitly marked optional)
	ParseMeta func(d *Domain, body string) AtomicMeta    // REQUIRED
	SkipNote  func(d *Domain, n Note) bool               // optional; default skips master + hub notes
	RenderHub func(d *Domain, name string, notes []Note, // nil => no Tier-2 phase
		order map[string][]string) string
	RenderMaster func(d *Domain, groups map[string][]Note) string     // REQUIRED
	BacklinkFor  func(d *Domain, bucket string) string                // optional; default: "[[" + bucket + "]]"
	SectionFor   func(d *Domain, bucket string, p AtomicParts) string // optional; default: p.Section

	// Duplicates is set by the orchestrator before each regen cycle and
	// holds the registry of atomic titles shared by ≥ 2 notes across all
	// managed domains. Renderers consult it via bear.AtomicWikilink to
	// switch ambiguous titles to bear://x-callback URL form. Nil-safe —
	// renderers handle the zero registry as "no duplicates known".
	Duplicates *DuplicateRegistry

	// ParseMasterTable, when set, makes the master a two-way contract: the
	// daemon reads the existing master before each regen and pulls a
	// title→bucket map out of its rendered table. Whenever an atomic's
	// canonical-header bucket disagrees with where it currently sits in the
	// master, the master wins — the daemon rewrites the atomic's canonical
	// header to match. Lets users move atomics between buckets by cut/paste
	// in the master itself, without ever opening the atomic note. Only
	// meaningful for flat-table domains (prose, aphorisms, lyrics-style);
	// nil for hub-of-hubs domains (poetry, articles).
	ParseMasterTable func(d *Domain, masterContent string) map[string]string

	// CanonicalTagFor, when set, lets a domain emit a per-atomic canonical
	// tag-line that varies by bucket — e.g. sub-tag preserving shapes where
	// `#<top>/<bucket>` replaces the static `#<top>`. Returning d.CanonicalTag
	// (or any value not starting with `#<top>`) is acceptable for the default
	// case. When nil, renderAtomicCanonical falls back to d.CanonicalTag for
	// every atomic — the existing flat-table / hub-routed shape.
	CanonicalTagFor func(d *Domain, bucket string) string

	// IsHubNote, when set, replaces the default H2-prefix-based hub
	// detection. Sub-tag preserving hub-routed domains key off the title
	// pattern `<top> · <bucket>` instead of an H2 marker. Receives the whole
	// Note so callback can match by title, content, or both.
	IsHubNote func(d *Domain, n Note) bool

	// HubTitleFor maps a bucket name to its Tier-2 hub note title. Defaults
	// to identity (bucket == title — the legacy hub-routed shape used by
	// llm/agents). Sub-tag preserving domains return `<top> · <bucket>` so
	// hub titles don't collide with arbitrary user notes that happen to
	// share a bucket name.
	HubTitleFor func(d *Domain, bucket string) string

	// BucketFromHubTitle inverts HubTitleFor — given a hub note title,
	// returns the bucket it represents. Returning "" signals "this title is
	// not one of our hubs" (caller skips). Default: identity. Used by
	// computeHubOverrides to decide which bucket a hub-bullet claims for
	// an atomic.
	BucketFromHubTitle func(d *Domain, title string) string

	// SkipAtomicsPass tells RunRegen to bypass the atomic-canonicalization
	// step. Used by umbrella domains (`#it`, `#library`, `#llm`) whose
	// "atoms" are actually atomics owned by sibling domains — running the
	// canonical-rewrite would clobber their per-domain canonical headers.
	// The grouping + master-render steps still run, so the umbrella master
	// can read live atom counts per child via its RenderMaster.
	SkipAtomicsPass bool

	// ParentMaster, when set, makes the domain's master emit its tag-line as
	// `#<tag> | [[<ParentMaster>]]` instead of bare `#<tag>` — giving the
	// reader a one-click jump up to the umbrella that aggregates this and
	// its sibling sub-domain masters. Auto-wired by NewUmbrellaDomain on
	// each child. Empty for top-level domains (umbrellas themselves and
	// personal/*).
	ParentMaster string

	// DefaultChild names the leaf-domain tag that the umbrella master's
	// "Нова нотатка" link targets. Spec component 4: without this field,
	// umbrella clicks land on the umbrella's bare tag (e.g. `#library`),
	// Bear creates a note with only that top-level tag, daemon's
	// SkipAtomicsPass=true means the note is never canonicalized, and
	// the new note becomes a permanent orphan. Required for any domain
	// with SkipAtomicsPass=true (every current umbrella); Validate
	// rejects an empty value or one that doesn't match a registered
	// child's Tag, OR a value pointing at a nested umbrella.
	//
	// Wired by NewUmbrellaDomain (factory) + bear/config dispatch from
	// the TOML `default_child` key. Leaf domains leave it empty.
	DefaultChild string

	// defaultChildDomain is the leaf *Domain pointer resolved by
	// NewUmbrellaDomain at factory time from DefaultChild + children.
	// newNoteLinkForDomain delegates to this leaf so the umbrella's
	// "Нова нотатка" click produces a leaf-tagged note with leaf-correct
	// canonical body (CanonicalTag, backlinkFor(UnknownBucket), IndexTitle)
	// instead of the umbrella's internal "_umbrella" placeholder. Nil for
	// leaf domains; non-nil only on umbrellas after successful construction.
	defaultChildDomain *Domain

	// QuickPlaceholderH1 is the literal H1 string ("Quicknote", "Article",
	// etc.) the master's new-note x-callback URL embeds as a marker in
	// the bootstrap-URL text= payload. The daemon's fast-pass
	// (ApplyPlaceholderRefresh) spots this marker post-click and swaps
	// it for a fresh timestamp.
	//
	// Empty string falls back to DefaultQuickPlaceholderH1 via
	// effectiveQuickPlaceholderH1 — every domain still emits the
	// bootstrap URL form; the override only customizes the visible
	// placeholder text.
	QuickPlaceholderH1 string

	// MasterSections, when non-empty, replaces the default 3-tier
	// hub-routed master with a vertical-sections layout. Sections
	// render top-to-bottom in declaration order; each section picks
	// the buckets it lists via an explicit bucket list, a script-class
	// predicate, or a catch-all (no predicate). Empty → domain keeps
	// the default master.
	MasterSections []MasterSection

	// validationError carries a factory-level error so Validate can
	// surface it without the test-helper having to crash on misconfig.
	// Set by NewUmbrellaDomainForTest. Production callers use
	// NewUmbrellaDomain which panics on the same error class.
	validationError error
}

// MasterSection is one entry of a hub-routed domain's vertical-sections
// master layout. Title surfaces in the section header; the predicate
// (Buckets / Script / catch-all) decides which Tier-2 buckets fold
// into this section. Mirrors the TOML `[[domain.master_section]]`
// block in bear/config/schema.go.
//
// Selection rules — set at most one:
//
//   - Buckets non-nil → explicit bucket-name match.
//   - Script != "" → first-letter script class match
//     ("latin" / "non-latin").
//   - Both empty → catch-all (claims every still-unclaimed bucket).
//
// CountMode selects what `(N)` in the section header reports:
//
//   - CountModeNotes (default): sum of note counts across the
//     section's buckets.
//   - CountModeBuckets: number of distinct buckets in the section.
//
// ShowBulletCounts toggles the `(count)` suffix on each bullet.
type MasterSection struct {
	Title            string
	Buckets          []string
	Script           string
	CountMode        CountMode
	ShowBulletCounts bool
}

// CountMode picks the counting strategy for MasterSection headers.
// Zero value is CountModeNotes, matching the most common renderer
// expectation (sum of atomics under the section).
type CountMode int

const (
	// CountModeNotes counts the total notes across every bucket the
	// section claims — answers "how many atomics live here?".
	CountModeNotes CountMode = iota
	// CountModeBuckets counts the distinct buckets the section claims
	// — answers "how many Tier-2 hubs does this section list?".
	CountModeBuckets
)

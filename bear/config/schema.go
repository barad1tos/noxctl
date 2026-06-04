// Package config decodes noxctl.toml into typed Catalog/Stanza
// structs and dispatches each stanza into a *domain.Domain via the
// closed 5-blueprint catalog.
//
// Architectural constraints:
//
//   - bear/config/ is the only place BurntSushi/toml is imported.
//   - snake_case TOML tags throughout.
//   - [[domain]] singular array-of-tables form.
//   - Single Stanza struct; blueprint-specific fields are pointer types
//     so the loader can distinguish "omitted" from "set to zero value".
//   - Errors aggregate via stdlib errors.Join.
//   - Closed dispatch map literal — never reflection.
//
// The pointer-types choice on optional fields is load-bearing.
// BurntSushi metadata.Undecoded distinguishes "field omitted" from
// "field set to zero value" only when the field is *T (with the
// `toml:"…"` tag). For optional fields we MUST distinguish — otherwise
// per-blueprint validation can't tell "user typed unknown_bucket = \"\""
// from "user omitted unknown_bucket altogether".
package config

import "time"

// Catalog is the decoded noxctl.toml document. Round-trip safe; no
// map[string]any anywhere.
type Catalog struct {
	Meta       Meta              `toml:"meta"`
	Domains    []Stanza          `toml:"domain"`
	Promotions []Promotion       `toml:"promotion"`
	Features   Features          `toml:"features"`
	I18N       map[string]string `toml:"i18n"`
}

// Meta is the [meta] table. version is required; locale defaults to
// "uk"; bear_db is optional override of Bear's default DB directory;
// daily_default_tag binds the operator's "untagged-on-create" tag
// for the auto-tag fast-pass.
type Meta struct {
	Version         string `toml:"version"`
	Locale          string `toml:"locale"`
	BearDB          string `toml:"bear_db"`
	DailyDefaultTag string `toml:"daily_default_tag"`
}

// Promotion is a single rule in the time-based promotion ladder.
// When an atomic note tagged `From` has a creation date older than
// the calendar boundary named by `Boundary`, the daemon rewrites
// its canonical tag-line to `To`. Operators define their own chain
// (daily→weekly→monthly→…) or leave the array empty to disable
// time-promotion entirely.
//
// Valid Boundary values: "day", "week", "month", "year". The
// matching helper functions in bear/calendar.go drive the actual
// threshold check.
type Promotion struct {
	From     string `toml:"from"`
	To       string `toml:"to"`
	Boundary string `toml:"boundary"`
}

// Features is the optional [features] toggle table. All toggles are
// pointer types so the loader can distinguish "omitted" from "set to
// false" when merging defaults vs explicit user overrides.
type Features struct {
	AutoTagDefault    *bool `toml:"auto_tag_default"`
	CrossDomainMoves  *bool `toml:"cross_domain_moves"`
	TimePromotion     *bool `toml:"time_promotion"`
	ForeignTagEscape  *bool `toml:"foreign_tag_escape"`
	DuplicateRegistry *bool `toml:"duplicate_registry"`
	// DomainBootstrap opts the universal fast-pass
	// canonicalization pre-pass on/off. Pointer so the loader can
	// distinguish "omitted" (inherit default ON) from "explicitly false"
	// (kill-switch). Daemon-side mirror lives at
	// `DaemonConfig.DomainBootstrap` and is wired via env > file >
	// default in `bear/config/daemon.go`.
	DomainBootstrap *bool `toml:"domain_bootstrap"`
}

// Stanza is a single [[domain]] array-of-tables entry. Required
// across every blueprint: Tag, IndexTitle, Blueprint. Blueprint-
// specific optional fields are pointer types so dispatch can
// reject a blueprint/field mismatch loudly instead of silently
// accepting a zero value.
//
// CanonicalTag and LastApply are decoded but not yet consumed by
// dispatch; they round-trip cleanly so (migration)
// and (apply state) can pick them up without a schema bump.
type Stanza struct {
	// Required across all blueprints.
	Tag        string `toml:"tag"`
	IndexTitle string `toml:"index_title"`
	Blueprint  string `toml:"blueprint"`

	// Common optionals.
	CanonicalTag  *string `toml:"canonical_tag"`
	UnknownBucket *string `toml:"unknown_bucket"`

	// grouped-vertical / hub-routed-with-subtag.
	Buckets *[]string `toml:"buckets"`

	// hub-routed only.
	HubH2Prefix *string `toml:"hub_h2_prefix"`

	// hub-routed: legacy H2 prefixes recognized as hub markers during
	// transitions (e.g. ["Поезії"] for library/poetry). Carried as an
	// ordered slice — positional semantics matter for the legacy H2
	// fallback walk in bear/routing.go::firstNonSectionH2.
	HubH2Legacy *[]string `toml:"hub_h2_legacy"`

	// hub-routed (poetry-style domains with own group + aliases).
	OwnGroup   *string   `toml:"own_group"`
	OwnAliases *[]string `toml:"own_aliases"`

	// hub-routed flags (poetry/articles family).
	LegacyAuthorFallback *bool `toml:"legacy_author_fallback"`
	StripLegacyAuthorH2  *bool `toml:"strip_legacy_author_h2"`

	// umbrella only — list of child Tag values resolved by the loader.
	Children *[]string `toml:"children"`

	// umbrella only — Tag of the child leaf the umbrella's "Нова нотатка"
	// link targets. Spec component 4: required for every umbrella so
	// clicks on the umbrella master create a note tagged into a leaf
	// domain instead of the bare top-level tag. Validated by the
	// dispatch path against the resolved children slice.
	DefaultChild *string `toml:"default_child"`

	// Reserved: hub-routed-with-subtag may grow a per-domain subtag
	// override here. Decoded but not yet consumed.
	Subtag *string `toml:"subtag"`

	// QuickPlaceholderH1 opts a flat-list domain into the x-callback
	// bootstrap URL pattern. When set, the domain's master "new-note"
	// link embeds the full canonical body in `text=` with a literal H1
	// marker matching this value (e.g. "Quicknote"); the daemon's
	// fast-pass swaps that marker for a fresh timestamp post-click.
	// Empty / unset disables the bootstrap form (legacy simple URL).
	QuickPlaceholderH1 *string `toml:"quick_placeholder_h1"`

	// MasterSections turns a hub-routed domain's master from the
	// default 3-tier layout into a vertical stack of named sections.
	// Each entry binds a title to a predicate that selects which
	// Tier-2 buckets fold into that section: explicit `buckets = [...]`,
	// a script-class match via `script = "latin"|"non-latin"`, or no
	// predicate at all (catch-all). Unset (block omitted) keeps the
	// default 3-tier master; declaring `master_section = []` with zero
	// sections is rejected at load — that would produce a silent blank
	// master at render time and the validator catches it instead.
	MasterSections *[]StanzaMasterSection `toml:"master_section"`

	// Reserved: apply path stamps the last successful apply
	// timestamp through the loader. Decoded but not yet consumed.
	LastApply *time.Time `toml:"last_apply"`
}

// StanzaMasterSection defines one vertical section of a hub-routed
// domain's master output. Exactly one selection rule should be set:
//
//   - Buckets: explicit list of bucket names that fold into this
//     section.
//   - Script: a first-letter script class that auto-filters buckets;
//     valid values are "latin" (the ASCII alphabet, FirstLetter
//     group "1") and "non-latin" (everything else — Cyrillic,
//     Greek, CJK, symbols, digits). Operators who need a finer
//     partition spell it via explicit `buckets = [...]`.
//   - Neither set → catch-all: any bucket not claimed by a previous
//     section lands here. At most one catch-all per domain (the last
//     section's catch-all swallows the unmapped remainder).
//
// Order matters: sections render top-to-bottom in declaration order,
// and earlier sections claim their buckets first.
//
// CountMode controls what the section header's `(N)` reports.
// Empty / unset is equivalent to "notes" and is accepted as a
// convenience for stanzas that don't need to override the default.
//   - "" or "notes" (default): sum of note counts across the
//     section's buckets — answers "how many atomics live under
//     here?".
//   - "buckets": number of distinct buckets — answers "how many
//     Tier-2 hubs does this section list?".
//
// ShowBulletCounts controls whether each bullet appends `(count)`
// after the wikilink. Defaults to true; set false to emit plain
// `[[bucket]]` bullets (artist-list style).
type StanzaMasterSection struct {
	Title            string   `toml:"title"`
	Buckets          []string `toml:"buckets"`
	Script           string   `toml:"script"`
	CountMode        string   `toml:"count_mode"`
	ShowBulletCounts *bool    `toml:"show_bullet_counts"`
}

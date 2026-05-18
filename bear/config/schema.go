// Package config decodes noxctl.toml into typed Catalog/Stanza
// structs and dispatches each stanza into a *bear.Domain via the
// closed 6-blueprint catalog.
//
// Decisions (locked in 01-CONTEXT.md):
//
//	D-01 lives at bear/config/, the only place BurntSushi/toml is imported
//	D-07 snake_case TOML tags throughout
//	D-08 [[domain]] singular array-of-tables form
//	D-09 single Stanza struct; blueprint-specific fields are pointer types
//	D-11 aggregate errors via stdlib errors.Join
//	D-12 closed dispatch map literal — never reflection
//
// The pointer-types choice on optional fields is load-bearing.
// BurntSushi metadata.Undecoded distinguishes "field omitted" from
// "field set to zero value" only when the field is *T (with the
// `toml:"…"` tag). For optional fields we MUST distinguish — otherwise
// per-blueprint validation can't tell "user typed unknown_bucket = """
// from "user omitted unknown_bucket altogether".
package config

import "time"

// Catalog is the decoded noxctl.toml document. Round-trip safe; no
// map[string]any anywhere — CFG-01 acceptance criterion.
type Catalog struct {
	Meta     Meta              `toml:"meta"`
	Domains  []Stanza          `toml:"domain"`
	Features Features          `toml:"features"`
	I18N     map[string]string `toml:"i18n"`
}

// Meta is the [meta] table. version is required; locale defaults to
// "uk" (CFG-03); bear_db is optional override of Bear's default DB
// directory.
type Meta struct {
	Version string `toml:"version"`
	Locale  string `toml:"locale"`
	BearDB  string `toml:"bear_db"`
}

// Features is the optional [features] toggle table. All toggles are
// pointer types so the loader can distinguish "omitted" from "set to
// false" — 's apply path will need that distinction when
// merging defaults vs explicit user overrides.
type Features struct {
	AutoTagDefault    *bool `toml:"auto_tag_default"`
	CrossDomainMoves  *bool `toml:"cross_domain_moves"`
	TimePromotion     *bool `toml:"time_promotion"`
	ForeignTagEscape  *bool `toml:"foreign_tag_escape"`
	DuplicateRegistry *bool `toml:"duplicate_registry"`
	// DomainBootstrap opts the universal fast-pass
	// canonicalization pre-pass on/off. Pointer per D-09 so the loader
	// can distinguish "omitted" (inherit default ON) from "explicitly
	// false" (kill-switch). Daemon-side mirror lives at
	// `DaemonConfig.DomainBootstrap` and is wired via env > file >
	// default in `bear/config/daemon.go`.
	DomainBootstrap *bool `toml:"domain_bootstrap"`
}

// Stanza is a single [[domain]] array-of-tables entry. Required
// across every blueprint: Tag, IndexTitle, Blueprint. Blueprint-
// specific optional fields are pointer types (D-09) so dispatch can
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

	// flat-table / grouped-vertical / hub-routed-with-subtag.
	Buckets *[]string `toml:"buckets"`

	// hub-routed only.
	HubH2Prefix *string `toml:"hub_h2_prefix"`

	// hub-routed: legacy H2 prefixes recognized as hub markers during
	// transitions (e.g. ["Поезії"] for library/poetry). Carried as an
	// ordered slice — positional semantics matter for the legacy H2
	// fallback walk in bear/core.go::firstNonSectionH2.
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

	// Renderer selects a closed Go renderer registered in
	// bear/custom/. Only meaningful when Blueprint == "custom"; the
	// dispatch validator rejects this field on every other blueprint
	// (D-06 — closed catalog, no scripting hatch).
	Renderer *string `toml:"renderer"`

	// Reserved: apply path stamps the last successful apply
	// timestamp through the loader. Decoded but not yet consumed.
	LastApply *time.Time `toml:"last_apply"`
}

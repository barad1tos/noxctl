package config

import (
	"fmt"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

// buildFunc constructs a *domain.Domain from a stanza. The
// resolveChildren callback is set only for umbrella blueprints; leaf
// builders ignore it.
type buildFunc func(s Stanza, resolveChildren func([]string) ([]*domain.Domain, error)) (*domain.Domain, error)

// dispatch is the closed 6-entry catalog that maps a blueprint string
// to the corresponding domain.New*Domain factory. Adding a seventh
// blueprint requires an explicit map entry plus a new builder; there
// is intentionally no reflection or struct-tag-driven grammar.
//
// The map is unexported so the loader is the only caller — Dispatch
// is the public entry point. DispatchSize is exported for tests.
var dispatch = map[string]buildFunc{
	"flat-list":              buildFlatList,
	"flat-table":             bucketedBuilder("flat-table", render.NewGroupedVerticalFlatDomain),
	"grouped-vertical":       bucketedBuilder("grouped-vertical", render.NewGroupedVerticalDomain),
	"hub-routed":             buildHubRouted,
	"hub-routed-with-subtag": buildHubRoutedSubTag,
	"umbrella":               buildUmbrella,
}

// bucketedBuilder returns a buildFunc bound to the given blueprint
// label and bear factory. The flat-table and grouped-vertical
// blueprints share the same contract; spelling them out as two
// closure literals in the dispatch map literal would trip dupl, and
// the factory function remains the only meaningful difference.
func bucketedBuilder(blueprint string,
	factory func(tag, indexTitle, unknownBucket string, buckets []string) *domain.Domain,
) buildFunc {
	return func(s Stanza, _ func([]string) ([]*domain.Domain, error)) (*domain.Domain, error) {
		return buildBucketed(blueprint, s, factory)
	}
}

// DispatchSize returns the number of entries in the closed catalog.
// Surfacing this through a function (not a constant) keeps the map
// itself the single source of truth — the test verifies the literal
// shape, not a hand-maintained count.
func DispatchSize() int { return len(dispatch) }

// validBlueprints is the human-readable enumeration appended to
// ErrUnknownBlueprint messages. Keep in sync with the dispatch map
// keys (the test enforces this).
const validBlueprints = "flat-list, flat-table, grouped-vertical, " +
	"hub-routed, hub-routed-with-subtag, umbrella"

// Dispatch maps a stanza's blueprint string to a *domain.Domain via the
// closed catalog. Returns an error wrapping ErrUnknownBlueprint when
// the blueprint is not one of the 6 supported values.
//
// The resolveChildren callback is invoked only for umbrella stanzas;
// the loader passes a function that resolves child Tag values to
// previously-built leaf Domains. Non-umbrella callers may pass nil.
func Dispatch(stanza Stanza, resolveChildren func(tags []string) ([]*domain.Domain, error)) (*domain.Domain, error) {
	builder, ok := dispatch[stanza.Blueprint]
	if !ok {
		return nil, fmt.Errorf("%w: %q (valid: %s)",
			ErrUnknownBlueprint, stanza.Blueprint, validBlueprints)
	}
	return builder(stanza, resolveChildren)
}

// optKey enumerates every optional [[domain]] field the schema
// recognizes. Each per-blueprint allow-list is a subset of this set;
// validateAgainstAllowList computes the forbidden complement and
// reports any that the user actually populated. Keeping the catalog
// of options in one place collapses the per-blueprint forbidden
// boilerplate to a single allow-list declaration.
type optKey string

const (
	optBuckets       optKey = "buckets"
	optUnknownBucket optKey = "unknown_bucket"
	optOwnGroup      optKey = "own_group"
	optOwnAliases    optKey = "own_aliases"
	optHubH2Prefix   optKey = "hub_h2_prefix"
	optHubH2Legacy   optKey = "hub_h2_legacy"
	optChildren      optKey = "children"
	optDefaultChild  optKey = "default_child"
	optSubtag        optKey = "subtag"
	optLegacyAuthor  optKey = "legacy_author_fallback"
	optStripAuthorH2 optKey = "strip_legacy_author_h2"
	optMasterSection optKey = "master_section"
	optQuickPlaceH1  optKey = "quick_placeholder_h1"
)

// allOptKeys lists every optional field once. Order is irrelevant for
// validation logic but stable for human-readable error messages.
var allOptKeys = []optKey{
	optBuckets, optUnknownBucket, optOwnGroup, optOwnAliases,
	optHubH2Prefix, optHubH2Legacy, optChildren, optDefaultChild, optSubtag,
	optLegacyAuthor, optStripAuthorH2,
	optMasterSection,
	optQuickPlaceH1,
}

// stanzaHas reports whether the user populated the given optional
// field in the stanza. Centralizing this dispatch in one switch keeps
// the per-builder code declarative.
func stanzaHas(s Stanza, k optKey) bool {
	switch k {
	case optBuckets:
		return s.Buckets != nil
	case optUnknownBucket:
		return s.UnknownBucket != nil
	case optOwnGroup:
		return s.OwnGroup != nil
	case optOwnAliases:
		return s.OwnAliases != nil
	case optHubH2Prefix:
		return s.HubH2Prefix != nil
	case optHubH2Legacy:
		return s.HubH2Legacy != nil
	case optChildren:
		return s.Children != nil
	case optDefaultChild:
		return s.DefaultChild != nil
	case optSubtag:
		return s.Subtag != nil
	case optLegacyAuthor:
		return s.LegacyAuthorFallback != nil
	case optStripAuthorH2:
		return s.StripLegacyAuthorH2 != nil
	case optMasterSection:
		return s.MasterSections != nil
	case optQuickPlaceH1:
		return s.QuickPlaceholderH1 != nil
	}
	return false
}

// validateBlueprintFields enforces a per-blueprint contract: the
// required keys MUST be populated; every optional key NOT in the
// allow-list MUST be unpopulated. Returns the first missing-required
// error (structurally incomplete > forbidden noise) or an aggregated
// forbidden-field error listing every offender.
func validateBlueprintFields(blueprint string, s Stanza, required []optKey, allowed []optKey) error {
	for _, r := range required {
		if !stanzaHas(s, r) {
			return fmt.Errorf("%s %q: %s is required", blueprint, s.Tag, r)
		}
	}
	allowSet := make(map[optKey]struct{}, len(allowed))
	for _, a := range allowed {
		allowSet[a] = struct{}{}
	}
	var bad []optKey
	for _, k := range allOptKeys {
		if _, ok := allowSet[k]; ok {
			continue
		}
		if stanzaHas(s, k) {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s %q: fields not allowed for this blueprint: %v",
			blueprint, s.Tag, bad)
	}
	return nil
}

// buildFlatList: REQUIRES tag, index_title (always required at the
// schema level — the loader checks them once before dispatch). Only
// quick_placeholder_h1 is allowed as an optional field — it flips the
// master "new-note" link to the x-callback bootstrap URL form (see
// domain.Domain.QuickPlaceholderH1).
func buildFlatList(s Stanza, _ func([]string) ([]*domain.Domain, error)) (*domain.Domain, error) {
	if err := validateBlueprintFields("flat-list", s, nil, []optKey{optQuickPlaceH1}); err != nil {
		return nil, err
	}
	d := render.NewFlatListDomain(s.Tag, s.IndexTitle)
	if s.QuickPlaceholderH1 != nil {
		d.QuickPlaceholderH1 = *s.QuickPlaceholderH1
	}
	return d, nil
}

// buildBucketed shares the buckets+unknown_bucket contract between
// flat-table and grouped-vertical. safety: the factory's
// positional args are filled BY NAME from the stanza, never by
// arg-order coincidence.
func buildBucketed(blueprint string, s Stanza,
	factory func(tag, indexTitle, unknownBucket string, buckets []string) *domain.Domain,
) (*domain.Domain, error) {
	if err := validateBlueprintFields(blueprint, s,
		[]optKey{optBuckets, optUnknownBucket},
		[]optKey{optBuckets, optUnknownBucket}); err != nil {
		return nil, err
	}
	return factory(s.Tag, s.IndexTitle, *s.UnknownBucket, *s.Buckets), nil
}

// buildHubRouted: REQUIRES unknown_bucket + hub_h2_prefix. Allows
// own_group / own_aliases / legacy flags + master_section blocks.
// When [[domain.master_section]] is present the domain swaps its
// master from the default 3-tier layout to the generic vertical-
// sections renderer driven by the predicates the operator declared.
func buildHubRouted(s Stanza, _ func([]string) ([]*domain.Domain, error)) (*domain.Domain, error) {
	if err := validateBlueprintFields("hub-routed", s,
		[]optKey{optUnknownBucket, optHubH2Prefix},
		[]optKey{
			optUnknownBucket, optHubH2Prefix, optHubH2Legacy,
			optOwnGroup, optOwnAliases,
			optLegacyAuthor, optStripAuthorH2,
			optMasterSection,
		}); err != nil {
		return nil, err
	}
	if err := validateMasterSections(s); err != nil {
		return nil, err
	}
	d := render.NewHubRoutedDomain(
		s.Tag, s.IndexTitle, *s.UnknownBucket, *s.HubH2Prefix,
		render.DefaultRenderMaster3Tier,
	)
	applyHubRoutedOptionals(d, s)
	applyMasterSections(d, s)
	return d, nil
}

// masterSectionEnumError formats a "unknown <field> %q" rejection
// for a hub-routed master_section enum (script, count_mode, etc.).
// Extracted to keep the per-enum check lines under the dupl
// threshold — the two call sites (script + count_mode) had nearly
// identical message shapes and were tripping the linter.
func masterSectionEnumError(tag string, idx int, title, field, got, valid string) error {
	return fmt.Errorf(
		"hub-routed %q: master_section[%d] %q has unknown %s %q (valid: %s)",
		tag, idx, title, field, got, valid)
}

// validateMasterSections enforces the per-section selection-rule
// constraints up-front so dispatch errors surface at load time, not
// at first render. An empty `master_section = []` block is rejected
// because it produces a silent blank master at render time; each
// section must set at most one of Buckets / Script; an unknown
// script or count_mode string is rejected with the accepted set in
// the error message; sections must carry a non-empty Title because
// empty headers would render as `(N)` which is operator-confusing.
func validateMasterSections(s Stanza) error {
	if s.MasterSections == nil {
		return nil
	}
	if len(*s.MasterSections) == 0 {
		return fmt.Errorf("hub-routed %q: master_section block is present but empty; "+
			"remove the block to keep the default 3-tier master, or add at least one section", s.Tag)
	}
	validScripts := map[string]struct{}{"latin": {}, "non-latin": {}}
	validCounts := map[string]struct{}{"": {}, "notes": {}, "buckets": {}}
	for i, sec := range *s.MasterSections {
		if sec.Title == "" {
			return fmt.Errorf("hub-routed %q: master_section[%d] is missing required `title`", s.Tag, i)
		}
		if len(sec.Buckets) > 0 && sec.Script != "" {
			return fmt.Errorf(
				"hub-routed %q: master_section[%d] %q sets both `buckets` and `script`; "+
					"pick exactly one selection rule",
				s.Tag, i, sec.Title)
		}
		if sec.Script != "" {
			if _, ok := validScripts[sec.Script]; !ok {
				return masterSectionEnumError(s.Tag, i, sec.Title, "script",
					sec.Script, "latin|non-latin")
			}
		}
		if _, ok := validCounts[sec.CountMode]; !ok {
			return masterSectionEnumError(s.Tag, i, sec.Title, "count_mode",
				sec.CountMode, "notes|buckets, empty = notes")
		}
	}
	return nil
}

// applyMasterSections copies the TOML master_section blocks onto the
// domain and swaps RenderMaster to the generic sectioned renderer.
// No-op when MasterSections is unset — the default 3-tier master
// stays in place.
func applyMasterSections(d *domain.Domain, s Stanza) {
	if s.MasterSections == nil {
		return
	}
	sections := make([]domain.MasterSection, len(*s.MasterSections))
	for i, sec := range *s.MasterSections {
		sections[i] = domain.MasterSection{
			Title:            sec.Title,
			Buckets:          sec.Buckets,
			Script:           sec.Script,
			CountMode:        countModeFromString(sec.CountMode),
			ShowBulletCounts: showBulletCountsDefault(sec.ShowBulletCounts),
		}
	}
	d.MasterSections = sections
	d.RenderMaster = render.SectionedMasterRenderer()
}

// countModeFromString maps the TOML enum string to the domain.CountMode
// constant. Empty → CountModeNotes (the renderer-friendly default).
// validateMasterSections rejects unknown strings before this runs.
func countModeFromString(s string) domain.CountMode {
	if s == "buckets" {
		return domain.CountModeBuckets
	}
	return domain.CountModeNotes
}

// ApplyMasterSectionsForTest exposes applyMasterSections to the
// external test package. Production callers reach the same logic
// through buildHubRouted; this seam lets a test drive two consecutive
// applies on the same *domain.Domain to pin the "no append on repeat"
// idempotency contract without going through full Dispatch (which
// constructs a fresh Domain each call and can't catch the regression).
func ApplyMasterSectionsForTest(d *domain.Domain, s Stanza) {
	applyMasterSections(d, s)
}

// showBulletCountsDefault resolves the *bool pointer to a plain bool.
// Unset → true so the common case (`[[bucket]] (N)` bullets) needs
// zero config; operators wanting plain wikilinks set false explicitly.
func showBulletCountsDefault(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}

// applyHubRoutedOptionals stamps the optional pointer-typed fields on
// the constructed Domain. Extracted to keep buildHubRouted under the
// gocognit ≤15 budget.
func applyHubRoutedOptionals(d *domain.Domain, s Stanza) {
	if s.OwnGroup != nil {
		d.OwnGroup = *s.OwnGroup
	}
	if s.OwnAliases != nil {
		d.OwnAliases = make(map[string]struct{}, len(*s.OwnAliases))
		for _, alias := range *s.OwnAliases {
			d.OwnAliases[alias] = struct{}{}
		}
	}
	if s.HubH2Legacy != nil {
		d.HubH2Legacy = append([]string(nil), *s.HubH2Legacy...)
	}
	if s.LegacyAuthorFallback != nil {
		d.LegacyAuthorFallback = *s.LegacyAuthorFallback
	}
	if s.StripLegacyAuthorH2 != nil {
		d.StripLegacyAuthorH2 = *s.StripLegacyAuthorH2
	}
}

// buildHubRoutedSubTag: REQUIRES buckets + unknown_bucket. Sub-tag
// preserving hubs key off `<top> · <bucket>` titles, so hub_h2_prefix
// is forbidden (the factory wires its own IsHubNote).
func buildHubRoutedSubTag(s Stanza, _ func([]string) ([]*domain.Domain, error)) (*domain.Domain, error) {
	if err := validateBlueprintFields("hub-routed-with-subtag", s,
		[]optKey{optBuckets, optUnknownBucket},
		[]optKey{optBuckets, optUnknownBucket, optSubtag}); err != nil {
		return nil, err
	}
	return render.NewHubRoutedSubTagDomain(s.Tag, s.IndexTitle, *s.UnknownBucket, *s.Buckets), nil
}

// buildUmbrella: REQUIRES children + default_child. Resolver maps each
// child Tag to a previously-built *domain.Domain (loader two-pass — leaf
// domains first, umbrellas last). The factory wires the reverse
// ParentMaster pointer on every child; that side effect is by design.
//
// default_child names the leaf the umbrella's "Нова нотатка" link
// targets (spec component 4); safeNewUmbrellaDomain converts the
// factory's panic-on-misconfig into an error so malformed TOML produces
// a clean error path instead of crashing the loader.
func buildUmbrella(s Stanza, resolveChildren func([]string) ([]*domain.Domain, error)) (*domain.Domain, error) {
	if err := validateBlueprintFields("umbrella", s,
		[]optKey{optChildren, optDefaultChild},
		[]optKey{optChildren, optDefaultChild}); err != nil {
		return nil, err
	}
	if resolveChildren == nil {
		return nil, fmt.Errorf("umbrella %q: resolveChildren callback is nil (loader bug)", s.Tag)
	}
	kids, err := resolveChildren(*s.Children)
	if err != nil {
		return nil, fmt.Errorf("umbrella %q: %w", s.Tag, err)
	}
	return safeNewUmbrellaDomain(s.Tag, s.IndexTitle, *s.DefaultChild, kids)
}

// safeNewUmbrellaDomain wraps render.NewUmbrellaDomain, recovering its
// panic-on-misconfig into a returned error. The factory panics so
// hardcoded callers fail fast at init; the TOML loader needs a soft
// error path so malformed user config produces a friendly message
// instead of crashing the daemon.
func safeNewUmbrellaDomain(tag, indexTitle, defaultChild string, kids []*domain.Domain) (d *domain.Domain, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("umbrella %q: %v", tag, r)
		}
	}()
	return render.NewUmbrellaDomain(tag, indexTitle, defaultChild, kids), nil
}

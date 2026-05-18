package config

import (
	"fmt"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/custom"
)

// buildFunc constructs a *bear.Domain from a stanza. The
// resolveChildren callback is set only for umbrella blueprints; leaf
// builders ignore it.
type buildFunc func(s Stanza, resolveChildren func([]string) ([]*bear.Domain, error)) (*bear.Domain, error)

// dispatch is the closed 7-entry catalog that maps a blueprint string
// to the corresponding bear.New*Domain factory. Adding an 8th
// blueprint requires an explicit map entry plus a new builder; there
// is intentionally no reflection or struct-tag-driven grammar (D-12).
//
// The "custom" entry (D-06) is itself a closed registry: the
// renderer name picks one of the Go-side renderers in bear/custom/.
// Adding a new custom renderer requires Go code + test, same gate as
// adding a new blueprint — NOT a scripting hatch.
//
// The map is unexported so the loader is the only caller — Dispatch
// is the public entry point. DispatchSize is exported for tests.
var dispatch = map[string]buildFunc{
	"flat-list":              buildFlatList,
	"flat-table":             bucketedBuilder("flat-table", bear.NewGroupedVerticalFlatDomain),
	"grouped-vertical":       bucketedBuilder("grouped-vertical", bear.NewGroupedVerticalDomain),
	"hub-routed":             buildHubRouted,
	"hub-routed-with-subtag": buildHubRoutedSubTag,
	"umbrella":               buildUmbrella,
	"custom":                 buildCustom,
}

// bucketedBuilder returns a buildFunc bound to the given blueprint
// label and bear factory. The flat-table and grouped-vertical
// blueprints share the same contract; spelling them out as two
// closure literals in the dispatch map literal would trip dupl, and
// the factory function remains the only meaningful difference.
func bucketedBuilder(blueprint string,
	factory func(tag, indexTitle, unknownBucket string, buckets []string) *bear.Domain,
) buildFunc {
	return func(s Stanza, _ func([]string) ([]*bear.Domain, error)) (*bear.Domain, error) {
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
	"hub-routed, hub-routed-with-subtag, umbrella, custom"

// Dispatch maps a stanza's blueprint string to a *bear.Domain via the
// closed catalog. Returns an error wrapping ErrUnknownBlueprint when
// the blueprint is not one of the 6 supported values.
//
// The resolveChildren callback is invoked only for umbrella stanzas;
// the loader passes a function that resolves child Tag values to
// previously-built leaf Domains. Non-umbrella callers may pass nil.
func Dispatch(stanza Stanza, resolveChildren func(tags []string) ([]*bear.Domain, error)) (*bear.Domain, error) {
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
	optRenderer      optKey = "renderer"
	optQuickPlaceH1  optKey = "quick_placeholder_h1"
)

// allOptKeys lists every optional field once. Order is irrelevant for
// validation logic but stable for human-readable error messages.
var allOptKeys = []optKey{
	optBuckets, optUnknownBucket, optOwnGroup, optOwnAliases,
	optHubH2Prefix, optHubH2Legacy, optChildren, optDefaultChild, optSubtag,
	optLegacyAuthor, optStripAuthorH2,
	optRenderer,
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
	case optRenderer:
		return s.Renderer != nil
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
// bear.Domain.QuickPlaceholderH1).
func buildFlatList(s Stanza, _ func([]string) ([]*bear.Domain, error)) (*bear.Domain, error) {
	if err := validateBlueprintFields("flat-list", s, nil, []optKey{optQuickPlaceH1}); err != nil {
		return nil, err
	}
	d := bear.NewFlatListDomain(s.Tag, s.IndexTitle)
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
	factory func(tag, indexTitle, unknownBucket string, buckets []string) *bear.Domain,
) (*bear.Domain, error) {
	if err := validateBlueprintFields(blueprint, s,
		[]optKey{optBuckets, optUnknownBucket},
		[]optKey{optBuckets, optUnknownBucket}); err != nil {
		return nil, err
	}
	return factory(s.Tag, s.IndexTitle, *s.UnknownBucket, *s.Buckets), nil
}

// buildHubRouted: REQUIRES unknown_bucket + hub_h2_prefix. Allows
// own_group / own_aliases / legacy flags. always wires
// DefaultRenderMaster3Tier; renderer identity is concern.
func buildHubRouted(s Stanza, _ func([]string) ([]*bear.Domain, error)) (*bear.Domain, error) {
	if err := validateBlueprintFields("hub-routed", s,
		[]optKey{optUnknownBucket, optHubH2Prefix},
		[]optKey{optUnknownBucket, optHubH2Prefix, optHubH2Legacy,
			optOwnGroup, optOwnAliases,
			optLegacyAuthor, optStripAuthorH2}); err != nil {
		return nil, err
	}
	d := bear.NewHubRoutedDomain(s.Tag, s.IndexTitle, *s.UnknownBucket, *s.HubH2Prefix, bear.DefaultRenderMaster3Tier)
	applyHubRoutedOptionals(d, s)
	return d, nil
}

// applyHubRoutedOptionals stamps the optional pointer-typed fields on
// the constructed Domain. Extracted to keep buildHubRouted under the
// gocognit ≤15 budget.
func applyHubRoutedOptionals(d *bear.Domain, s Stanza) {
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
func buildHubRoutedSubTag(s Stanza, _ func([]string) ([]*bear.Domain, error)) (*bear.Domain, error) {
	if err := validateBlueprintFields("hub-routed-with-subtag", s,
		[]optKey{optBuckets, optUnknownBucket},
		[]optKey{optBuckets, optUnknownBucket, optSubtag}); err != nil {
		return nil, err
	}
	return bear.NewHubRoutedSubTagDomain(s.Tag, s.IndexTitle, *s.UnknownBucket, *s.Buckets), nil
}

// buildUmbrella: REQUIRES children + default_child. Resolver maps each
// child Tag to a previously-built *bear.Domain (loader two-pass — leaf
// domains first, umbrellas last). The factory wires the reverse
// ParentMaster pointer on every child; that side effect is by design.
//
// default_child names the leaf the umbrella's "Нова нотатка" link
// targets (spec component 4); safeNewUmbrellaDomain converts the
// factory's panic-on-misconfig into an error so malformed TOML produces
// a clean error path instead of crashing the loader.
func buildUmbrella(s Stanza, resolveChildren func([]string) ([]*bear.Domain, error)) (*bear.Domain, error) {
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

// safeNewUmbrellaDomain wraps bear.NewUmbrellaDomain, recovering its
// panic-on-misconfig into a returned error. The factory panics so
// hardcoded callers fail fast at init; the TOML loader needs a soft
// error path so malformed user config produces a friendly message
// instead of crashing the daemon.
func safeNewUmbrellaDomain(tag, indexTitle, defaultChild string, kids []*bear.Domain) (d *bear.Domain, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("umbrella %q: %v", tag, r)
		}
	}()
	return bear.NewUmbrellaDomain(tag, indexTitle, defaultChild, kids), nil
}

// buildCustom dispatches "custom" stanzas (D-06). The renderer
// name picks one of the closed Go-side renderers in bear/custom/; the
// registered Apply func stamps non-default callbacks (and, for agents,
// extra primitive flags) onto a base hub-routed Domain.
//
// All three current custom renderers (lyrics, quotes, agents) are
// hub-routed-shaped at the Domain level — they require unknown_bucket
// + hub_h2_prefix and accept the standard hub-routed optional fields.
// If a future custom renderer needs a fundamentally different base,
// the right move is a separate base-builder, not a switch in here.
//
// ErrUnknownRenderer flows out as a wrap chain so callers test via
// errors.Is(err, custom.ErrUnknownRenderer) — never string-match
// (from the recurring-pitfalls catalog).
func buildCustom(s Stanza, _ func([]string) ([]*bear.Domain, error)) (*bear.Domain, error) {
	if s.Renderer == nil {
		return nil, fmt.Errorf("custom %q: renderer is required for blueprint \"custom\"", s.Tag)
	}
	if err := validateBlueprintFields("custom", s,
		[]optKey{optRenderer, optUnknownBucket, optHubH2Prefix},
		[]optKey{
			optRenderer, optUnknownBucket, optHubH2Prefix, optHubH2Legacy,
			optOwnGroup, optOwnAliases,
			optLegacyAuthor, optStripAuthorH2,
		}); err != nil {
		return nil, err
	}
	// RENDERER INVARIANT GUARD (WR-01): certain custom renderers fix
	// flags that are otherwise hub-routed optionals. Because the order
	// below is `c.Apply(d) → applyHubRoutedOptionals(d, s)`, a TOML
	// stanza that overrode one of those flags would silently win over
	// the renderer's invariant — surfacing later as corpus corruption
	// at runtime (e.g. agents body has `## Metadata` H2;
	// LegacyAuthorFallback=true would misread it as the bucket name).
	// Fail loudly at validate-time instead.
	if err := rejectCustomFlagConflicts(s); err != nil {
		return nil, err
	}
	c, err := custom.Lookup(*s.Renderer)
	if err != nil {
		return nil, fmt.Errorf("custom %q: %w", s.Tag, err)
	}
	d := bear.NewHubRoutedDomain(s.Tag, s.IndexTitle, *s.UnknownBucket, *s.HubH2Prefix,
		bear.DefaultRenderMaster3Tier)
	c.Apply(d)
	applyHubRoutedOptionals(d, s)
	return d, nil
}

// rejectCustomFlagConflicts surfaces a validate-time error when a TOML
// stanza tries to override a flag the custom renderer's Apply func
// explicitly stamps. agents flips both legacy flags to false (see
// bear/custom/agents.go::applyAgents); any TOML-side override on those
// fields would silently win because applyHubRoutedOptionals runs AFTER
// c.Apply. Returns nil for unrestricted renderers (lyrics, quotes).
//
// from the recurring-pitfalls catalog: callers test the disposition
// via the returned error message, not by string-matching on the
// surfaced fields elsewhere in the pipeline.
func rejectCustomFlagConflicts(s Stanza) error {
	if s.Renderer == nil {
		return nil
	}
	switch *s.Renderer {
	case "agents":
		if s.LegacyAuthorFallback != nil || s.StripLegacyAuthorH2 != nil {
			return fmt.Errorf("custom %q renderer=%q forbids "+
				"legacy_author_fallback / strip_legacy_author_h2 overrides "+
				"(renderer enforces both=false; see bear/custom/agents.go)",
				s.Tag, *s.Renderer)
		}
	}
	return nil
}

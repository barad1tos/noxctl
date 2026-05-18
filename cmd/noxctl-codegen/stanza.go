package main

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/custom"
	"github.com/barad1tos/noxctl/registry"
)

// domainToStanza translates a *bear.Domain back into the config.Stanza
// shape the loader would produce when dispatching it. bucketsHint feeds
// the per-blueprint `buckets` field (positional factory arg, NOT stored
// on Domain — codegen reads it from the existing roman.toml round-trip,
// see harvestBuckets in main.go).
//
// Detection heuristics fire in most-specific-first order. The order
// matters: e.g. NewGroupedVerticalDomain ALSO sets CanonicalTagFor, so
// we MUST check for the hub-routed-with-subtag-specific `IsHubNote`
// signal first.
//
// Returns a wrapped error when the domain doesn't fit any of the 7
// known blueprints. The wrap message names the tag so a regression
// against the closed catalog points at the offender immediately.
func domainToStanza(d *bear.Domain, bucketsHint []string) (config.Stanza, error) {
	if name, ok := detectCustomRenderer(d); ok {
		return stanzaForCustom(d, name), nil
	}
	if d.SkipAtomicsPass {
		return stanzaForUmbrella(d)
	}
	if d.IsHubNote != nil {
		return stanzaForHubRoutedSubTag(d, bucketsHint), nil
	}
	if d.HubH2Prefix != "" {
		return stanzaForHubRouted(d), nil
	}
	if d.CanonicalTagFor != nil && d.ParseMasterTable != nil {
		return stanzaForGroupedVertical(d, bucketsHint), nil
	}
	if d.ParseMasterTable != nil {
		return stanzaForFlatTable(d, bucketsHint), nil
	}
	if d.UnknownBucket == "_flat" {
		return stanzaForFlatList(d), nil
	}
	return config.Stanza{}, fmt.Errorf(
		"unrecognized blueprint shape (HubH2Prefix=%q UnknownBucket=%q "+
			"HasCanonicalTagFor=%t HasIsHubNote=%t HasParseMasterTable=%t)",
		d.HubH2Prefix, d.UnknownBucket,
		d.CanonicalTagFor != nil, d.IsHubNote != nil, d.ParseMasterTable != nil,
	)
}

// detectCustomRenderer probes every registered custom renderer by
// applying it to a fresh hub-routed Domain shaped exactly like the
// input's primitive surface, then comparing the resulting RenderMaster
// pointer to the input's. Match → renderer name. Inspired by the
// pattern in bear/config/dispatch_custom_test.go::TestDispatchCustomRoundTrip,
// which uses reflect.ValueOf(...).Pointer as the canonical "renderer
// identity" check for closure-typed fields.
//
// Returns (name, true) on first match, ("", false) otherwise. Iterates
// over a stable sorted order (custom.Names) so a ambiguous double-match
// — which would itself be a custom-registry bug — is deterministic.
func detectCustomRenderer(d *bear.Domain) (string, bool) {
	if d.RenderMaster == nil {
		return "", false
	}
	target := reflect.ValueOf(d.RenderMaster).Pointer()
	for _, name := range custom.Names() {
		c, err := custom.Lookup(name)
		if err != nil {
			continue
		}
		probe := bear.NewHubRoutedDomain(d.Tag, d.IndexTitle, d.UnknownBucket, d.HubH2Prefix,
			bear.DefaultRenderMaster3Tier)
		c.Apply(probe)
		if reflect.ValueOf(probe.RenderMaster).Pointer() == target {
			return name, true
		}
	}
	return "", false
}

// stanzaForCustom assembles the [[domain]] entry for a domain whose
// RenderMaster matched a custom renderer. Primitive surface mirrors
// hub-routed (the base factory the buildCustom dispatcher uses); the
// only extras are Blueprint="custom" and Renderer=&name.
func stanzaForCustom(d *bear.Domain, name string) config.Stanza {
	s := config.Stanza{
		Tag:        d.Tag,
		IndexTitle: d.IndexTitle,
		Blueprint:  "custom",
		Renderer:   new(name),
	}
	applyHubRoutedPrimitives(&s, d)
	return s
}

// stanzaForUmbrella builds the umbrella entry. children are recovered
// by walking the leaf slice and picking every domain whose ParentMaster
// points at d.IndexTitle — the bear.NewUmbrellaDomain factory stamps
// that field as a side effect when assembling the umbrella. DefaultChild
// (spec component 4) round-trips through the dedicated Domain field the
// factory stores at construction time.
func stanzaForUmbrella(d *bear.Domain) (config.Stanza, error) {
	children := childrenOfUmbrella(d)
	if len(children) == 0 {
		return config.Stanza{}, fmt.Errorf(
			"umbrella %q has no children — registry.All() must include leaves before umbrellas",
			d.Tag)
	}
	if d.DefaultChild == "" {
		return config.Stanza{}, fmt.Errorf(
			"umbrella %q has empty DefaultChild — registry must declare default_child for every umbrella",
			d.Tag)
	}
	return config.Stanza{
		Tag:          d.Tag,
		IndexTitle:   d.IndexTitle,
		Blueprint:    "umbrella",
		Children:     &children,
		DefaultChild: new(d.DefaultChild),
	}, nil
}

// childrenOfUmbrella iterates the canonical leaf slice and collects
// every Tag whose ParentMaster matches the umbrella's IndexTitle. Order
// follows registry.Hardcoded (the canonical main.go order); umbrellas
// in registry.All come AFTER their children so the ParentMaster
// side-effect has already been applied at registry-construction time.
func childrenOfUmbrella(d *bear.Domain) []string {
	var children []string
	for _, leaf := range registry.Hardcoded() {
		if leaf.ParentMaster == d.IndexTitle {
			children = append(children, leaf.Tag)
		}
	}
	return children
}

// stanzaForHubRoutedSubTag assembles the entry for personal/claude-style
// domains. Buckets propagate from the hint (factory positional arg, not
// stored on Domain).
func stanzaForHubRoutedSubTag(d *bear.Domain, buckets []string) config.Stanza {
	return bucketedStanza("hub-routed-with-subtag", d, buckets)
}

// stanzaForHubRouted assembles the entry for plain hub-routed domains
// (library/poetry, library/articles, library/lyrics-as-hub-routed pre-04-02,
// etc.). LegacyAuthorFallback / StripLegacyAuthorH2 are emitted ONLY
// when they DIFFER from the NewHubRoutedDomain default of true — the
// existing examples/roman.toml convention emits them unconditionally for
// every hub-routed entry. We follow that convention so the byte-equality
// test stays meaningful.
func stanzaForHubRouted(d *bear.Domain) config.Stanza {
	s := config.Stanza{
		Tag:        d.Tag,
		IndexTitle: d.IndexTitle,
		Blueprint:  "hub-routed",
	}
	applyHubRoutedPrimitives(&s, d)
	return s
}

// applyHubRoutedPrimitives stamps the optional pointer-typed fields
// shared by hub-routed and custom stanzas: UnknownBucket, HubH2Prefix,
// OwnGroup, OwnAliases, LegacyAuthorFallback, StripLegacyAuthorH2.
//
// Convention (matches the existing roman.toml hand-written corpus):
// - UnknownBucket and HubH2Prefix always emitted when non-empty
// (required for the dispatch validator anyway).
// - OwnGroup + OwnAliases emitted only when OwnGroup is non-empty.
// - LegacyAuthorFallback + StripLegacyAuthorH2 emitted UNCONDITIONALLY
// because the existing hand-written corpus does so — the bytes-
// equal acceptance test reads off that convention.
func applyHubRoutedPrimitives(s *config.Stanza, d *bear.Domain) {
	if d.UnknownBucket != "" {
		s.UnknownBucket = new(d.UnknownBucket)
	}
	if d.HubH2Prefix != "" {
		s.HubH2Prefix = new(d.HubH2Prefix)
	}
	if len(d.HubH2Legacy) > 0 {
		cp := append([]string(nil), d.HubH2Legacy...)
		s.HubH2Legacy = &cp
	}
	if d.OwnGroup != "" {
		s.OwnGroup = new(d.OwnGroup)
		s.OwnAliases = aliasesAsSlicePtr(d.OwnAliases)
	}
	// RENDERER INVARIANT (WR-01): agents enforces both legacy flags =
	// false (see bear/custom/agents.go::applyAgents). Codegen MUST NOT
	// emit those keys for renderer="agents" so the validate-time guard
	// in bear/config/dispatch.go::rejectCustomFlagConflicts stays
	// happy — emitting them with the renderer's own enforced value
	// would still trip the guard because the guard checks presence,
	// not value.
	if s.Renderer != nil && *s.Renderer == "agents" {
		return
	}
	s.LegacyAuthorFallback = new(d.LegacyAuthorFallback)
	s.StripLegacyAuthorH2 = new(d.StripLegacyAuthorH2)
}

// stanzaForGroupedVertical handles personal/english-style domains:
// flat-table semantics with sub-tag preservation. Buckets are required.
func stanzaForGroupedVertical(d *bear.Domain, buckets []string) config.Stanza {
	return bucketedStanza("grouped-vertical", d, buckets)
}

// stanzaForFlatTable handles library/aphorisms-style domains: flat-table
// semantics without sub-tag preservation. Buckets are required.
func stanzaForFlatTable(d *bear.Domain, buckets []string) config.Stanza {
	return bucketedStanza("flat-table", d, buckets)
}

// bucketedStanza shares the (Tag, IndexTitle, Blueprint, UnknownBucket,
// Buckets) assembly across hub-routed-with-subtag, grouped-vertical, and
// flat-table — three blueprints that all carry a positional bucket list
// recovered from the existing roman.toml round-trip (buckets are not
// stored on *bear.Domain, only on the original factory call).
func bucketedStanza(blueprint string, d *bear.Domain, buckets []string) config.Stanza {
	s := config.Stanza{
		Tag:           d.Tag,
		IndexTitle:    d.IndexTitle,
		Blueprint:     blueprint,
		UnknownBucket: new(d.UnknownBucket),
	}
	if buckets != nil {
		cp := append([]string(nil), buckets...)
		s.Buckets = &cp
	}
	return s
}

// stanzaForFlatList handles llm/characters-style domains: no buckets,
// no Tier-2, single alphabetical bullet list at master level. The only
// optional field that round-trips through the TOML schema is
// quick_placeholder_h1 (opts into the x-callback bootstrap URL).
func stanzaForFlatList(d *bear.Domain) config.Stanza {
	s := config.Stanza{
		Tag:        d.Tag,
		IndexTitle: d.IndexTitle,
		Blueprint:  "flat-list",
	}
	if d.QuickPlaceholderH1 != "" {
		s.QuickPlaceholderH1 = new(d.QuickPlaceholderH1)
	}
	return s
}

// aliasesAsSlicePtr collects map keys in stable lexical order so the
// TOML output is deterministic. Returns nil when the map is empty so
// the encoder elides the field via omitempty.
func aliasesAsSlicePtr(m map[string]struct{}) *[]string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return &out
}

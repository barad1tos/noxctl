// Package cliutil contains the boundary helpers that bridge
// `bear/config/` (TOML schema) and `bear/engine/` (runtime contracts).
// Lives outside `cmd/noxctl/` so its functions are reachable from
// `tests/bear/cliutil/` — package `main` has no external test surface
// under this project's "tests live under tests/<pkg>/" rule.
package cliutil

import (
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/fastpass"
)

// FeaturesFromCatalog copies `*config.Catalog.Features` (pointer fields,
// distinguishing "omitted in TOML" from "set to false") into the flat
// `engine.Features` struct. Defaults: every pre-pass ON — operators get
// every fast-pass running out of the box unless they explicitly opt out
// via the catalog.
func FeaturesFromCatalog(cat *config.Catalog) engine.Features {
	f := engine.Features{
		AutoTagDefault:    true,
		CrossDomainMoves:  true,
		TimePromotion:     true,
		ForeignTagEscape:  true,
		DuplicateRegistry: true,
		DomainBootstrap:   true,
	}
	if cat == nil {
		return f
	}
	if cat.Features.AutoTagDefault != nil {
		f.AutoTagDefault = *cat.Features.AutoTagDefault
	}
	if cat.Features.CrossDomainMoves != nil {
		f.CrossDomainMoves = *cat.Features.CrossDomainMoves
	}
	if cat.Features.TimePromotion != nil {
		f.TimePromotion = *cat.Features.TimePromotion
	}
	if cat.Features.ForeignTagEscape != nil {
		f.ForeignTagEscape = *cat.Features.ForeignTagEscape
	}
	if cat.Features.DuplicateRegistry != nil {
		f.DuplicateRegistry = *cat.Features.DuplicateRegistry
	}
	if cat.Features.DomainBootstrap != nil {
		f.DomainBootstrap = *cat.Features.DomainBootstrap
	}
	return f
}

// DailyDefaultTagFromCatalog returns the operator-chosen default tag for
// untagged notes, if the catalog defines one.
func DailyDefaultTagFromCatalog(cat *config.Catalog) string {
	if cat == nil {
		return ""
	}
	return cat.Meta.DailyDefaultTag
}

// PromotionRulesFromCatalog maps catalog promotion stanzas onto the runtime
// fast-pass rule shape.
func PromotionRulesFromCatalog(cat *config.Catalog) []fastpass.PromotionRule {
	if cat == nil || len(cat.Promotions) == 0 {
		return nil
	}
	out := make([]fastpass.PromotionRule, 0, len(cat.Promotions))
	for _, promotion := range cat.Promotions {
		out = append(out, fastpass.PromotionRule{
			From:     promotion.From,
			To:       promotion.To,
			Boundary: promotion.Boundary,
		})
	}
	return out
}

// ResolveFeatures applies the operator-override step on top of the
// catalog-derived Features stamp returned by FeaturesFromCatalog. When
// `dc.Sources["DomainBootstrap"]` is anything other than SourceDefault
// (operator set the value via env REGEN_DOMAIN_BOOTSTRAP or daemon.toml
// `[daemon].domain_bootstrap` — the env-vs-file distinction is collapsed
// by config.LoadDaemon before reaching here), `dc.DomainBootstrap` wins
// over the catalog setting. Otherwise the catalog value stands.
//
// Operator kill-switch invariant: env/daemon-toml override MUST win over
// the catalog so an operator with a broken or unloadable catalog still
// has a path to disable the pre-pass without redeploy.
//
// Only DomainBootstrap currently has env/daemon-toml override surface —
// the other five Features fields resolve catalog > default only. New
// override paths added in bear/config/daemon.go MUST be mirrored here.
//
// The `ok` guard on the Sources lookup is deliberate: a partial
// DaemonConfig (e.g., a test fixture constructed without the Sources
// map) reads `""` from a nil map, which compares not-equal to
// SourceDefault. Without the guard the override branch would silently
// fire and clobber the catalog with `dc`'s zero-value DomainBootstrap.
func ResolveFeatures(cat *config.Catalog, dc config.DaemonConfig) engine.Features {
	f := FeaturesFromCatalog(cat)
	if src, ok := dc.Sources["DomainBootstrap"]; ok && src != config.SourceDefault {
		f.DomainBootstrap = dc.DomainBootstrap
	}
	return f
}

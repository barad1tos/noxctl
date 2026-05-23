// Package cliutil contains the boundary helpers that bridge
// `bear/config/` (TOML schema) and `bear/engine/` (runtime contracts).
// Lives outside `cmd/noxctl/` so its functions are reachable from
// `tests/bear/cliutil/` — package `main` has no external test surface
// under this project's "tests live under tests/<pkg>/" rule.
package cliutil

import (
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/engine"
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

// ResolveFeatures composes the catalog feature stamp with the daemon-toml
// override layer. Precedence chain (highest to lowest):
//
//  1. env REGEN_DOMAIN_BOOTSTRAP  — captured at LoadDaemon time, surfaces as dc.Sources["DomainBootstrap"] = SourceEnv
//  2. daemon.toml [daemon].domain_bootstrap — surfaces as SourceFile
//  3. catalog [features].domain_bootstrap — overlaid in FeaturesFromCatalog
//  4. ship default (true)
//
// Currently only `DomainBootstrap` has a daemon-toml/env override path —
// the other five features resolve only `catalog > default`. Returning the
// resolved Features struct lets the daemon entry point thread one value
// instead of layering overrides inline.
func ResolveFeatures(cat *config.Catalog, dc config.DaemonConfig) engine.Features {
	f := FeaturesFromCatalog(cat)
	if dc.Sources["DomainBootstrap"] != config.SourceDefault {
		f.DomainBootstrap = dc.DomainBootstrap
	}
	return f
}

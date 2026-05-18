// Package library holds per-tag Bear domain configurations for the
// `library/*` tag family (poetry, articles, lyrics, aphorisms, prose, quotes).
// Each file declares one `var FooDomain = &bear.Domain{...}` literal plus its
// render callbacks. The Bear framework lives in the sibling
// `github.com/barad1tos/noxctl/bear` package; siblings under
// `github.com/barad1tos/noxctl/llm` host the `llm/*` family.
package library

import "github.com/barad1tos/noxctl/bear"

// PoetryDomain handles the library/poetry tag with the 3-tier model:
// atomic poems → per-author Hub notes → master ✱ Поезія index. Atomics
// optionally carry a section path in their canonical header (used for H3/H4
// nesting inside the per-author Hub).
//
// Wires the standard 3-tier defaults from the bear package; only the
// poetry-specific knobs (own group, legacy hub heading, transition flags)
// live here.
var PoetryDomain = &bear.Domain{
	Tag:          "library/poetry",
	CanonicalTag: "#library/poetry",
	IndexTitle:   bear.T("library.poetry.index"),
	OwnGroup:     bear.T("library.poetry.own-group"),
	OwnAliases: map[string]struct{}{
		bear.T("library.poetry.own-alias.short"): {},
		bear.T("library.poetry.own-group"):       {},
	},
	UnknownBucket: bear.T("library.poetry.unknown"),
	HubH2Prefix:   bear.T("library.poetry.h2-prefix"),
	HubH2Legacy:   []string{bear.T("library.poetry.h2-legacy")},

	LegacyAuthorFallback: true, // pre-canonical poetry notes still carry "## <Author>" in body
	StripLegacyAuthorH2:  true, // canonicalizer drops the legacy author H2 from body

	ParseMeta:    bear.DefaultParseMetaCanonical,
	RenderHub:    bear.DefaultRenderHub3Tier,
	RenderMaster: bear.DefaultRenderMaster3Tier,
	// BacklinkFor + SectionFor: defaults are correct (per-author hub link; preserve parsed section).
}

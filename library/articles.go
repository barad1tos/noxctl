package library

import "github.com/barad1tos/noxctl/bear"

// ArticlesDomain handles the library/articles tag with the same 3-tier model
// as PoetryDomain: atomic articles → per-author Hub notes → master ✱ Статті
// index. Author-Hub titles carry a "(статті)" suffix to avoid wikilink
// collisions with the poetry domain's per-author hubs (e.g. `Бродский` is a
// poetry hub, articles use `Бродский (статті)`).
//
// Wires the standard 3-tier defaults from the bear package; only the
// articles-specific knobs (own group, hub-H2 word, content-preservation
// flags) live here.
var ArticlesDomain = &bear.Domain{
	Tag:           "library/articles",
	CanonicalTag:  "#library/articles",
	IndexTitle:    bear.T("library.articles.index"),
	OwnGroup:      bear.T("library.articles.own-group"),
	OwnAliases:    map[string]struct{}{bear.T("library.articles.own-group"): {}},
	UnknownBucket: bear.T("library.articles.unknown"),
	HubH2Prefix:   bear.T("library.articles.h2-prefix"),

	LegacyAuthorFallback: false, // articles often quote H2 headings; never misread as authors
	StripLegacyAuthorH2:  false, // preserve content H2s (chapter headings, sections, etc.)

	ParseMeta:    bear.DefaultParseMetaCanonical,
	RenderHub:    bear.DefaultRenderHub3Tier,
	RenderMaster: bear.DefaultRenderMaster3Tier,
	// BacklinkFor + SectionFor: defaults are correct (per-author hub link; preserve parsed section).
}

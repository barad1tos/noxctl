// Package llm holds per-tag Bear domain configurations for the `llm/*` tag
// family (agents, characters, rules, tips). Mirrors the layout of
// `github.com/barad1tos/noxctl/library`: one `var FooDomain = &bear.Domain{...}`
// literal per file. Shared abstractions live in `github.com/barad1tos/noxctl/bear`.
package llm

import "github.com/barad1tos/noxctl/bear"

// CharactersDomain handles llm/characters with a flat-list master: every
// atomic gets a single bullet in `✱ LLM Персонажі`, no Tier-2 hubs, no
// sub-grouping. Each atomic backlinks at the master directly. Suited to
// the corpus's modest scale (≤10 character notes) where additional layers
// would only add navigation hops.
var CharactersDomain = bear.NewFlatListDomain("llm/characters", bear.T("llm.characters.index"))

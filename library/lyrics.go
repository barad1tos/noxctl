package library

import "github.com/barad1tos/noxctl/bear"

// LyricsDomain handles the library/lyrics tag with a hybrid 3-tier model:
// atomic songs → per-artist Hub notes → master ✱ Тексти. Atomics carry
// `## <Artist>` legacy H2 in body which the daemon canonicalizes into
// `#library/lyrics | [[<Artist>]]` on first regen, stripping the H2.
//
// Master renders two vertical sections — `## Іноземні (M)` for Latin-named
// artists and `## СНГ (N)` for Cyrillic — each followed by alphabetised
// bullet links to the per-artist Tier-2 hub. Phone-friendly vertical scroll.
//
// D-06: the renderer body lives in bear/custom/lyrics.go and is
// stamped onto this Domain via the closed registry (see buildCustomHub
// Routed in custom_shim.go). The struct shape kept here exists ONLY so
// cmd/regen-watchd, registry.Hardcoded, and the round-trip test still
// see a *bear.Domain literal byte-equivalent to pre-migration; the
// entire library/ package is deleted in the D-12 atomic commit.
var LyricsDomain *bear.Domain = buildCustomHubRouted(
	"library/lyrics",
	bear.T("library.lyrics.index"),
	bear.T("library.lyrics.unknown"),
	bear.T("library.lyrics.h2-prefix"),
	"lyrics",
)

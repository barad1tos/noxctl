package library

import "github.com/barad1tos/noxctl/bear"

// QuotesDomain handles the library/quotes tag with the standard 3-tier model:
// atomic quotes → per-source Tier-2 hub → master `✱ Цитати`. The atomic body
// carries a `## <Source>` H2 (Баш / It_happens / future sources) which the
// daemon promotes into the canonical header's wikilink target on first regen
// and strips from the body. After canonicalization each atomic backlinks at
// its source-hub: `#library/quotes | [[Баш]]`.
//
// Master rendering uses a custom `## Джерела` heading instead of the default
// `## Автори` — sources aren't authors, and the user-facing wording matters.
//
// Volume note: ~444 quotes today (427 Баш + 17 It_happens). A flat-table
// master would render unreadably long cells; per-source hubs paginate the
// corpus naturally and keep the master to a 2-line index.
//
// D-06: renderer body lives in bear/custom/quotes.go; this var
// stays only as the brownfield primitive-equivalence anchor (built via
// buildCustomHubRouted in custom_shim.go) and is removed in the
// D-12 atomic deletion.
var QuotesDomain *bear.Domain = buildCustomHubRouted(
	"library/quotes",
	bear.T("library.quotes.index"),
	bear.T("library.quotes.unknown"),
	bear.T("library.quotes.h2-prefix"),
	"quotes",
)

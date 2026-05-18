package custom

import (
	"fmt"

	"github.com/barad1tos/noxctl/bear"
)

// init installs the "quotes" renderer into the closed registry. Same
// pattern as bear/custom/lyrics.go — Go's same-package init order
// runs i18n.go first, so bear.T(...) lookups succeed at any later
// var-init time in importing packages.
func init() { RegisterMaster("quotes", renderQuotesMaster) }

// renderQuotesMaster produces ✱ Цитати with a `## Джерела (N)` section
// listing each source-hub with its quote count. Mirrors
// DefaultRenderMaster3Tier's structure but renames the section to fit the
// quotes domain's semantics — sources, not authors.
//
//	# ✱ Цитати
//	#library/quotes
//	---
//	## Джерела (444)
//	- [[Баш]] (427)
//	- [[It_happens]] (17)
func renderQuotesMaster(d *bear.Domain, groups map[string][]bear.Note) string {
	total := 0
	sources := make([]string, 0, len(groups))
	for source := range groups {
		sources = append(sources, source)
		total += len(groups[source])
	}
	bear.SortTitles(sources)
	bullets := make([]string, len(sources))
	for index, source := range sources {
		bullets[index] = fmt.Sprintf("[[%s]] (%d)", source, len(groups[source]))
	}
	return bear.RenderVerticalSections(d, []bear.Section{{
		Header:  fmt.Sprintf("%s (%d)", bear.T("library.quotes.section.sources"), total),
		Bullets: bullets,
	}})
}

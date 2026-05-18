package library

import "github.com/barad1tos/noxctl/bear"

// AphorismsDomain handles the library/aphorisms tag with a 2-tier model:
// source-hub notes (one per author/source, contain aphorism quotes inline as
// user-managed content) → master `✱ Афоризми` index. No Tier-2 hubs exist;
// the daemon only canonicals each source-hub's header and regenerates the
// master from the source-hubs' categories.
//
// Source-hub canonical header:
//
//	#library/aphorisms | [[✱ Афоризми]] | <Category>
//
// where Category is one of {Книги, Кіно, Ігри}. New categories appended in
// canonical headers surface in the master automatically — fully data-driven.
var AphorismsDomain = bear.NewGroupedVerticalFlatDomain(
	"library/aphorisms",
	bear.T("library.aphorisms.index"),
	bear.T("library.aphorisms.unknown"), // sensible default when ParseMeta returns empty section
	[]string{
		bear.T("library.aphorisms.bucket.books"),
		bear.T("library.aphorisms.bucket.movies"),
		bear.T("library.aphorisms.bucket.games"),
	},
)

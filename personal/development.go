package personal

import "github.com/barad1tos/noxctl/bear"

// DevelopmentDomain — 2 buckets, only 2 atoms today; tiny but registered
// per user's "include-all" preference. Grouped-vertical for consistency.
var DevelopmentDomain = bear.NewGroupedVerticalDomain(
	"development",
	bear.T("personal.development.index"),
	bear.T("common.unknown.other"),
	[]string{"ayu-jetbrains", "genreupdater"},
)

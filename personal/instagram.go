package personal

import "github.com/barad1tos/noxctl/bear"

// InstagramDomain — 2 buckets, ≤10 atoms; grouped-vertical sections.
var InstagramDomain = bear.NewGroupedVerticalDomain(
	"instagram",
	bear.T("personal.instagram.index"),
	bear.T("common.unknown.other"),
	[]string{"templates", "posts"},
)

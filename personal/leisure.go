package personal

import "github.com/barad1tos/noxctl/bear"

// LeisureDomain — 3 buckets, ≤10 atoms; grouped-vertical.
var LeisureDomain = bear.NewGroupedVerticalDomain(
	"leisure",
	bear.T("personal.leisure.index"),
	bear.T("common.unknown.other"),
	[]string{"cafe", "books", "gifts"},
)

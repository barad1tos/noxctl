package personal

import "github.com/barad1tos/noxctl/bear"

// TravelDomain — 1 bucket today (ukraine); grouped-vertical with one
// section + "інше" overflow. New buckets (e.g. "abroad", "plans") can
// be appended.
var TravelDomain = bear.NewGroupedVerticalDomain(
	"travel",
	bear.T("personal.travel.index"),
	bear.T("common.unknown.other"),
	[]string{"ukraine"},
)

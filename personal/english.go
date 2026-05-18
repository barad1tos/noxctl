package personal

import "github.com/barad1tos/noxctl/bear"

// EnglishDomain — 5 buckets, 30+ atoms; grouped-vertical master fits on
// one phone screen-worth of vertical scroll per `## bucket` section.
var EnglishDomain = bear.NewGroupedVerticalDomain(
	"english",
	bear.T("personal.english.index"),
	bear.T("common.unknown.other"),
	[]string{"homework", "rules", "vocabulary", "adverbs", "prepositions"},
)

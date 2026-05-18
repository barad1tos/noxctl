package personal

import "github.com/barad1tos/noxctl/bear"

// HealthDomain — 5 buckets, ≤15 atoms; grouped-vertical.
var HealthDomain = bear.NewGroupedVerticalDomain(
	"health",
	bear.T("personal.health.index"),
	bear.T("common.unknown.other"),
	[]string{"meals", "medicine", "psyche", "food", "training"},
)

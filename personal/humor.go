package personal

import "github.com/barad1tos/noxctl/bear"

// HumorDomain — 3 buckets, ≤10 atoms; grouped-vertical. The "it" bucket is
// unrelated to the top-level `it/*` tag family — it lives inside the humor
// canonical-header sub-tag (`#humor/it`), separate namespace.
var HumorDomain = bear.NewGroupedVerticalDomain(
	"humor",
	bear.T("personal.humor.index"),
	bear.T("common.unknown.other"),
	[]string{"articles", "it", "typologies"},
)

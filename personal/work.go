package personal

import "github.com/barad1tos/noxctl/bear"

// WorkDomain — 2 buckets but the `tasks` cell grows long enough that the
// flat-table layout overflows phone width. Switched to grouped-vertical
// while keeping sub-tag preservation (atoms still carry `#work/<sub>`).
var WorkDomain = bear.NewGroupedVerticalDomain(
	"work",
	bear.T("personal.work.index"),
	bear.T("common.unknown.other"),
	[]string{"questions", "tasks"},
)

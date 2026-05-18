// Package personal holds Bear domain configurations for the user's
// general-purpose tag families that don't belong to library/, llm/, or it/.
// Layout form is chosen per domain by atom count:
//
// - 1-N buckets, ≤30 atoms → bear.NewGroupedVerticalDomain (vertical
// `## bucket (N)` sections with bullets, sub-tags preserved). Used by
// english, health, leisure, humor, work, instagram, travel,
// development.
//
// - 7+ buckets / 30+ atoms → bear.NewHubRoutedSubTagDomain (master = list
// of Tier-2 hub wikilinks; each hub a separate note with bullet list of
// its atomics). Used for claude.
//
// All forms preserve sub-tags: every atomic carries `#<top>/<bucket>` as
// its canonical-header first token, so Bear's sidebar still shows the
// 2-level tag tree.
package personal

import "github.com/barad1tos/noxctl/bear"

// ClaudeDomain is the largest personal domain (60+ atoms across 7 buckets)
// — too tall for a single grouped-vertical master, so it gets per-bucket
// Tier-2 hub notes (`claude · sessions`, `claude · memory`,...).
//
// User can move atoms between buckets by cut/paste of the bullet inside any
// hub note; on the next regen the daemon rewrites the atomic's canonical
// sub-tag (`#claude/sessions` → `#claude/memory`) to track the bullet.
var ClaudeDomain = bear.NewHubRoutedSubTagDomain(
	"claude",
	bear.T("personal.claude.index"),
	bear.T("common.unknown.other"),
	[]string{"sessions", "memory", "decisions", "concepts", "research", "pets", "tasks"},
)

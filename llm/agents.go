package llm

import (
	"fmt"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/custom"
)

// AgentsDomain handles the llm/agents tag with a poetry-style 3-tier
// underneath: atomic agent definitions → per-category Tier-2 Hub →
// master `✱ LLM Агенти`. Categories are languages (csharp, python,...)
// and orchestration areas (business, data, devops,...) — kept flat as
// 3rd-segment buckets in the canonical header rather than nested tag paths
// (`llm/agents/csharp` etc) which produce a 3-level tag tree the user
// found unreadable.
//
// The master diverges from the default per-bucket bullet list: it groups
// the 35 buckets by domain into a 5-column markdown table for easier
// scanning. Buckets remain at their normal Tier-2 hubs — the table just
// reshuffles how the master surfaces them.
//
// Atomic canonical header:
//
//	#llm/agents | [[<Category>]]
//
// LegacyAuthorFallback is FALSE because agent atomics often start with
// `## Metadata` H2 — the legacy fallback would misread that as the author
// name. The one-shot migration script rewrote every legacy
// `#llm/agents/<X>` and `#llm/languages/<X>` body tag-line into the
// canonical form above, so the fallback is unreachable in practice.
//
// D-06: renderer body + agentsGroups data + applyAgents callback
// stamps live in bear/custom/agents.go. This var stays only as the
// brownfield primitive-equivalence anchor — applyAgents flips
// LegacyAuthorFallback / StripLegacyAuthorH2 back to FALSE so the
// resulting Domain matches the pre-migration struct literal field-for-
// field. Removed in the D-12 atomic deletion.
var AgentsDomain = mustBuildAgents()

// mustBuildAgents constructs the agents Domain via the standard
// hub-routed factory (carrying all primitive fields the round-trip
// test asserts) and then asks bear/custom to stamp the renderer +
// LegacyAuthorFallback/StripLegacyAuthorH2 deltas. Failed lookup
// panics with the tag — the registration is unconditional, a missing
// entry is a build regression worth a stack trace.
func mustBuildAgents() *bear.Domain {
	d := bear.NewHubRoutedDomain(
		"llm/agents",
		bear.T("llm.agents.index"),
		"uncategorized",
		bear.T("llm.agents.h2-prefix"),
		nil, // RenderMaster stamped via custom.Apply below
	)
	c, err := custom.Lookup("agents")
	if err != nil {
		panic(fmt.Sprintf("llm/agents: %v", err))
	}
	c.Apply(d)
	return d
}

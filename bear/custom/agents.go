package custom

import (
	"fmt"

	"github.com/barad1tos/noxctl/bear"
)

// init installs the "agents" renderer. Unlike lyrics/quotes, agents
// overrides more than just RenderMaster: the original llm/agents.go
// var literal flips LegacyAuthorFallback + StripLegacyAuthorH2 to
// false (agent atomics start with `## Metadata` H2 — content, not
// authors). applyAgents stamps every non-default callback by NAME so
// the brownfield primitive shape stays byte-equal (— never
// positional after a refactor).
func init() {
	Register(CustomDomain{Name: "agents", Apply: applyAgents})
}

// applyAgents stamps the llm/agents-specific deltas onto the base
// hub-routed Domain produced by NewHubRoutedDomain. Each stamp lands
// on a NAMED field so symmetric *bool pairs (LegacyAuthorFallback /
// StripLegacyAuthorH2) cannot accidentally swap roles.
//
// Why the deltas exist:
// - LegacyAuthorFallback=false: agent bodies often start with
// `## Metadata` H2 — the legacy fallback would misread that as
// the bucket name and re-canonicalize atoms into a "Metadata"
// bucket, corrupting the corpus.
// - StripLegacyAuthorH2=false: the same H2s carry content the
// curator wants preserved; stripping them would erase data.
//
// RenderMaster swaps to the column-table layout that groups buckets
// by curator-defined macro-categories (Розробка/Інфра/Якість/...).
func applyAgents(d *bear.Domain) {
	d.RenderMaster = renderAgentsMaster
	d.LegacyAuthorFallback = false
	d.StripLegacyAuthorH2 = false
}

// agentsGroups defines the master table's column layout. Each group
// bundles the buckets the user wants surfaced together; ordering inside
// the slice fixes left-to-right column order. Adding a new bucket
// without updating this list lands it in the synthetic
// llm.agents.column.other column appended at runtime — surfaces
// unmapped buckets so the user notices and decides where they go.
//
// Initialized once via buildAgentsGroups at package init (after
// bear.T's catalog is populated by bear/i18n.go's init); Go guarantees
// same-package init runs before cross-package var init.
var agentsGroups = buildAgentsGroups()

// buildAgentsGroups assembles the static llm/agents column layout. The
// titles are pulled from bear.T(...) so the catalog is the single source
// of truth for user-facing strings (I18N-01..03).
func buildAgentsGroups() []agentsGroup {
	return []agentsGroup{
		{
			Title: bear.T("llm.agents.column.development"),
			Buckets: []string{
				"angular", "cpp", "csharp", "django", "dotnet", "elixir", "flutter",
				"go", "java", "javascript", "kotlin", "laravel", "nextjs", "php",
				"powershell", "python", "react", "ruby", "rust", "spring", "sql",
				"swift", "typescript", "vue",
				"development", "dx", "meta",
			},
		},
		{
			Title:   bear.T("llm.agents.column.infra-data"),
			Buckets: []string{"infrastructure", "devops", "data"},
		},
		{
			Title:   bear.T("llm.agents.column.quality"),
			Buckets: []string{"quality"},
		},
		{
			Title:   bear.T("llm.agents.column.orchestration"),
			Buckets: []string{"orchestration"},
		},
		{
			Title:   bear.T("llm.agents.column.business"),
			Buckets: []string{"business", "research", "domains"},
		},
	}
}

// agentsGroup is the value-typed row of agentsGroups. Title surfaces
// in the master section header; Buckets enumerates which third-segment
// values fold into this column.
type agentsGroup struct {
	Title   string
	Buckets []string
}

// renderAgentsMaster produces the ✱ LLM Агенти master as a stack of
// vertical `## <Group> (N)` sections — each followed by a bullet list
// of that group's per-language Tier-2 hub wikilinks with their atom
// counts. Phone-friendly: no horizontal table overflow, scrollable
// section by section.
//
//	# ✱ LLM Агенти
//	#llm/agents
//	---
//	## Розробка (53)
//	- [[angular]] (1)
//	- [[cpp]] (1)
//	-...
//
//	## Інфра / Дані (26)
//	- [[data]] (12)
//	-...
func renderAgentsMaster(d *bear.Domain, groups map[string][]bear.Note) string {
	return bear.RenderVerticalSections(d, agentsSections(groups))
}

// agentsSections maps the curator-defined macro-categories
// (Розробка/Інфра/Якість/Координація/Бізнес) to bear.Section. Each section's
// header carries the sum-of-atom-counts across its languages; bullets are
// `[[<language>]] (count)` per Tier-2 hub. Empty groups drop out.
func agentsSections(groups map[string][]bear.Note) []bear.Section {
	cols := buildAgentsColumns(groups)
	sections := make([]bear.Section, 0, len(cols))
	for _, col := range cols {
		present := bucketsWithNotes(col.Buckets, groups)
		if len(present) == 0 {
			continue
		}
		bear.SortTitles(present)
		colTotal := 0
		bullets := make([]string, len(present))
		for index, bucket := range present {
			count := len(groups[bucket])
			colTotal += count
			bullets[index] = fmt.Sprintf("[[%s]] (%d)", bucket, count)
		}
		sections = append(sections, bear.Section{
			Header:  fmt.Sprintf("%s (%d)", col.Title, colTotal),
			Bullets: bullets,
		})
	}
	return sections
}

// buildAgentsColumns returns the rendered column sequence: every group
// from agentsGroups in declared order, plus a trailing
// llm.agents.column.other column for any bucket present in `groups`
// that wasn't mapped explicitly.
func buildAgentsColumns(groups map[string][]bear.Note) []agentsGroup {
	known := make(map[string]struct{})
	for _, group := range agentsGroups {
		for _, bucket := range group.Buckets {
			known[bucket] = struct{}{}
		}
	}

	var extras []string
	for bucket := range groups {
		if _, isKnown := known[bucket]; isKnown {
			continue
		}
		extras = append(extras, bucket)
	}
	bear.SortTitles(extras)

	cols := make([]agentsGroup, 0, len(agentsGroups)+1)
	cols = append(cols, agentsGroups...)
	if len(extras) > 0 {
		cols = append(cols, agentsGroup{Title: bear.T("llm.agents.column.other"), Buckets: extras})
	}
	return cols
}

// bucketsWithNotes filters a bucket list down to those that actually have
// notes in `groups`. Empty buckets are skipped so the master cell never
// emits a stale `[[csharp]] (0)` placeholder for buckets the user emptied.
func bucketsWithNotes(buckets []string, groups map[string][]bear.Note) []string {
	out := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		if items, ok := groups[bucket]; ok && len(items) > 0 {
			out = append(out, bucket)
		}
	}
	return out
}

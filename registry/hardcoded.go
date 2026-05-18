// Package registry holds the hardcoded *bear.Domain literals that
// existed before 's TOML migration. It is the SINGLE import-site
// for those domains during the bridge window — cmd/regen-watchd
// and the equivalence test both consume it.
//
// D-12 commitment: this entire package is deleted in the same
// atomic commit that removes library/llm/it/personal/quicknote.
//
// Layering: the package lives at top-level (sibling of library/, not
// under bear/) on purpose — D-01 layering forbids bear/ from
// importing bear/config/, and pulling library/ etc. into a bear/registry/
// subpackage would create exactly that kind of one-way import that's
// hard to audit later. Top-level is unambiguous and disappears cleanly
// in D-12.
package registry

import (
	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/it"
	"github.com/barad1tos/noxctl/library"
	"github.com/barad1tos/noxctl/llm"
	"github.com/barad1tos/noxctl/personal"
	"github.com/barad1tos/noxctl/quicknote"
)

// Hardcoded returns the 27 leaf domains in the same order as
// cmd/regen-watchd/main.go's `domains` slice. Copy is intentional —
// the slice is small and callers may mutate it.
func Hardcoded() []*bear.Domain {
	return []*bear.Domain{
		library.PoetryDomain,
		library.AphorismsDomain,
		library.ArticlesDomain,
		library.LyricsDomain,
		library.ProseDomain,
		library.QuotesDomain,
		llm.AgentsDomain,
		llm.CharactersDomain,
		llm.RulesDomain,
		llm.TipsDomain,
		it.DomainsDomain,
		it.VendorsDomain,
		it.TechnologiesDomain,
		personal.ClaudeDomain,
		personal.EnglishDomain,
		personal.WorkDomain,
		personal.HealthDomain,
		personal.LeisureDomain,
		personal.HumorDomain,
		personal.InstagramDomain,
		personal.TravelDomain,
		personal.DevelopmentDomain,
		quicknote.DailyDomain,
		quicknote.WeeklyDomain,
		quicknote.MonthlyDomain,
		quicknote.YearlyDomain,
		quicknote.DecadalDomain,
	}
}

// HardcodedUmbrellas returns the 4 umbrella domains. SIDE EFFECT: each
// child's ParentMaster is overwritten by bear.NewUmbrellaDomain — the
// same effect cmd/regen-watchd/main.go produces during init. Callers
// that want the side effect (everyone in the production path) call
// Hardcoded FIRST so the leaves exist, then HardcodedUmbrellas.
//
// The umbrella order matches cmd/regen-watchd/main.go verbatim:
// it, library, llm, quicknote. IndexTitle strings are copied verbatim
// from main.go ("✱ IT", "✱ Library", "✱ LLM", "✱ Quicknote") so the
// per-domain ParentMaster stamping is byte-identical to the legacy
// daemon path.
func HardcodedUmbrellas() []*bear.Domain {
	return []*bear.Domain{
		// it umbrella → it/domains: the broadest leaf catches generic IT
		// notes Roman drops without a sub-tag in mind.
		bear.NewUmbrellaDomain("it", "✱ IT", "it/domains", []*bear.Domain{
			it.DomainsDomain,
			it.VendorsDomain,
			it.TechnologiesDomain,
		}),
		// library umbrella → library/poetry: highest-traffic literary leaf
		// and the historical default destination for #library clicks.
		bear.NewUmbrellaDomain("library", "✱ Library", "library/poetry", []*bear.Domain{
			library.PoetryDomain,
			library.AphorismsDomain,
			library.ArticlesDomain,
			library.LyricsDomain,
			library.ProseDomain,
			library.QuotesDomain,
		}),
		// llm umbrella → llm/agents: primary working surface (agent specs);
		// characters/rules/tips are reference material populated by hand.
		bear.NewUmbrellaDomain("llm", "✱ LLM", "llm/agents", []*bear.Domain{
			llm.AgentsDomain,
			llm.CharactersDomain,
			llm.RulesDomain,
			llm.TipsDomain,
		}),
		// quicknote umbrella → quicknote/daily: time-promotion's source
		// bucket; new notes start as daily and graduate via the calendar
		// pipeline (weekly → monthly → yearly → decadal).
		bear.NewUmbrellaDomain("quicknote", "✱ Quicknote", "quicknote/daily", []*bear.Domain{
			quicknote.DailyDomain,
			quicknote.WeeklyDomain,
			quicknote.MonthlyDomain,
			quicknote.YearlyDomain,
			quicknote.DecadalDomain,
		}),
	}
}

// All returns leaves followed by umbrellas in canonical order. This is
// the sole consumer-facing entry — cmd/regen-watchd/main.go and
// tests/bear/config/roman_corpus_test.go both call it.
//
// Order contract: indices 0..26 are leaves; indices 27..30 are
// umbrellas. The contract is locked by registry/hardcoded_test.go's
// TestHardcodedRegistryAllPreservesOrder.
func All() []*bear.Domain {
	leaves := Hardcoded()
	umbrellas := HardcodedUmbrellas()
	out := make([]*bear.Domain, 0, len(leaves)+len(umbrellas))
	out = append(out, leaves...)
	out = append(out, umbrellas...)
	return out
}

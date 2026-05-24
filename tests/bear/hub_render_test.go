// Package bear_test — hub-render pipeline coverage.
//
// DefaultRenderHub3Tier is the RenderHub callback for 3-tier domains (poetry,
// articles): it turns a bucket's atoms into the per-bucket hub note an operator
// reads — `### Section (N)` groups with bullets, preserving any manual bullet
// reorder across regen. The whole ordering.go pipeline (GroupNotesBySection,
// NestSections, RenderSectionGroup, RenderNoteList, ApplyOrder,
// reorderForOutput, emitInPriorOrder, insertAlphabetically) sat at 0% because
// no test drove a hub render. These tests close that through the public
// callback boundary — never poking the private ordering helpers directly.
//
//cyrillic:permit
package bear_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

// poetryHubDomain is a minimal 3-tier domain shaped just enough for
// DefaultRenderHub3Tier: ParseMeta reads the section from the canonical
// line's 3rd segment, IndexTitle gates the backlink target, HubH2Prefix
// labels the Tier-2 count line. Mirrors Roman's #library/poetry setup.
//
//cyrillic:permit
func poetryHubDomain() *domain.Domain {
	return &domain.Domain{
		Tag:          "library/poetry",
		CanonicalTag: "#library/poetry",
		IndexTitle:   "✱ Поезії",
		HubH2Prefix:  "Вірші",
		ParseMeta:    render.DefaultParseMetaCanonical,
	}
}

// poetryAtom builds a hub atom under the "Романтизм" bucket whose canonical
// line's 3rd segment (`section`) is what ParseMeta reads as Section.
//
//cyrillic:permit
func poetryAtom(id, title, section string) domain.Note {
	return domain.Note{
		ID:      id,
		Title:   title,
		Content: "# " + title + "\n#library/poetry | [[Романтизм]] | " + section + "\n---\nтіло\n",
	}
}

// TestRenderHub3Tier_GroupsBySectionWithCounts pins the core hub render: atoms
// are grouped under `### <section> (N)` headers with their titles as bullets.
// User-facing bug if this regresses: the per-bucket hub note loses its section
// structure and the operator sees an undifferentiated bullet dump.
//
//cyrillic:permit
func TestRenderHub3Tier_GroupsBySectionWithCounts(t *testing.T) {
	d := poetryHubDomain()
	notes := []domain.Note{
		poetryAtom("a1", "Сонет до ночі", "Любовна лірика"),
		poetryAtom("a2", "Освідчення", "Любовна лірика"),
		poetryAtom("a3", "До народу", "Громадянська"),
	}

	out := render.DefaultRenderHub3Tier(d, "Романтизм", notes, nil)

	if !strings.Contains(out, "### Любовна лірика (2)") {
		t.Errorf("missing 2-count section header for Любовна лірика; got:\n%s", out)
	}
	if !strings.Contains(out, "### Громадянська (1)") {
		t.Errorf("missing 1-count section header for Громадянська; got:\n%s", out)
	}
	for _, title := range []string{"Сонет до ночі", "Освідчення", "До народу"} {
		if !strings.Contains(out, "[["+title+"]]") {
			t.Errorf("missing bullet for %q; got:\n%s", title, out)
		}
	}
}

// TestRenderHub3Tier_PreservesUserBulletOrder pins the order-stability
// contract: when an existing hub already lists bullets in a manual
// (non-alphabetical) order, a re-render must keep that order. User-facing bug
// if this regresses: every regen reshuffles the operator's hand-curated bullet
// order back to alphabetical, undoing their edits.
//
//cyrillic:permit
func TestRenderHub3Tier_PreservesUserBulletOrder(t *testing.T) {
	d := poetryHubDomain()
	notes := []domain.Note{
		poetryAtom("a1", "Алі", "Вірші"),
		poetryAtom("a2", "Беата", "Вірші"),
		poetryAtom("a3", "Віктор", "Вірші"),
	}
	// Operator's manual order: Віктор, Алі, Беата (not alphabetical).
	existingOrder := map[string][]string{"Вірші": {"Віктор", "Алі", "Беата"}}

	out := render.DefaultRenderHub3Tier(d, "Романтизм", notes, existingOrder)

	posViktor := strings.Index(out, "[[Віктор]]")
	posAli := strings.Index(out, "[[Алі]]")
	posBeata := strings.Index(out, "[[Беата]]")
	if posViktor < 0 || posAli < 0 || posBeata < 0 {
		t.Fatalf("not all bullets rendered; got:\n%s", out)
	}
	if posViktor >= posAli || posAli >= posBeata {
		t.Errorf("manual order Віктор→Алі→Беата not preserved (positions %d,%d,%d); got:\n%s",
			posViktor, posAli, posBeata, out)
	}
}

// TestRenderHub3Tier_SplicesNewcomerIntoExistingOrder pins the splice rule: a
// bullet not present in the existing order is inserted rather than dropped,
// while the explicitly-ordered bullets keep their relative order. User-facing
// bug if this regresses: a freshly-added atom silently vanishes from the hub
// because it wasn't in the prior bullet order.
//
//cyrillic:permit
func TestRenderHub3Tier_SplicesNewcomerIntoExistingOrder(t *testing.T) {
	d := poetryHubDomain()
	notes := []domain.Note{
		poetryAtom("a1", "Алі", "Вірші"),
		poetryAtom("a2", "Беата", "Вірші"),
		poetryAtom("a3", "Віктор", "Вірші"),
	}
	// Existing order knows only Віктор, Алі; Беата is the newcomer.
	existingOrder := map[string][]string{"Вірші": {"Віктор", "Алі"}}

	out := render.DefaultRenderHub3Tier(d, "Романтизм", notes, existingOrder)

	for _, title := range []string{"Алі", "Беата", "Віктор"} {
		if !strings.Contains(out, "[["+title+"]]") {
			t.Errorf("newcomer/known bullet %q dropped; got:\n%s", title, out)
		}
	}
	if posViktor, posAli := strings.Index(out, "[[Віктор]]"), strings.Index(out, "[[Алі]]"); posViktor > posAli {
		t.Errorf("existing order Віктор→Алі not preserved when splicing a newcomer; got:\n%s", out)
	}
}

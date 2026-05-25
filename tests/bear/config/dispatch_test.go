package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
)

// sliceStrPtr keeps test fixture construction concise — pointer-typed
// optional []string fields make repetition unavoidable otherwise.
// Single-string fixtures use Go 1.26 `new(value)` directly at call sites.
func sliceStrPtr(in ...string) *[]string { return new(append([]string(nil), in...)) }

// noResolver returns a resolveChildren that never gets called. Use it
// for non-umbrella stanzas where the dispatcher must not invoke the
// resolver.
func noResolver() func([]string) ([]*domain.Domain, error) {
	return nil
}

// TestDispatchMapSize: the closed catalog has EXACTLY 5 declarative
// blueprints. Adding or removing one requires an explicit map edit
// plus a new builder; this test is the canary.
func TestDispatchMapSize(t *testing.T) {
	if got := config.DispatchSize(); got != 5 {
		t.Fatalf("dispatch map size = %d, want 5 (closed catalog)", got)
	}
}

// dispatchCase couples a stanza with the resolver the dispatcher needs
// for that blueprint (only umbrella populates resolver).
type dispatchCase struct {
	stanza   config.Stanza
	resolver func([]string) ([]*domain.Domain, error)
}

// validDispatchCases produces one minimal-but-valid case per
// blueprint. Extracted so the table is data, not control-flow — keeps
// the test body small and dodges the dupl flag that would otherwise
// fire on the per-blueprint struct literals.
func validDispatchCases() map[string]dispatchCase {
	libraryPoetry := &domain.Domain{
		Tag: "library/poetry", CanonicalTag: "#library/poetry",
		IndexTitle: "✱ Поезія",
	}
	bucketsTwo := func(a, b string) *[]string { return sliceStrPtr(a, b) }
	return map[string]dispatchCase{
		"flat-list": {stanza: config.Stanza{
			Tag: "llm/characters", IndexTitle: "✱ Characters", Blueprint: "flat-list",
		}},
		"grouped-vertical (sub-tag/top-level)": {stanza: config.Stanza{
			Tag: "reading", IndexTitle: "✱ Reading", Blueprint: "grouped-vertical",
			UnknownBucket: new("Other"), Buckets: bucketsTwo("Books", "Talks"),
		}},
		"grouped-vertical (flat/2-level)": {stanza: config.Stanza{
			Tag: "library/aphorisms", IndexTitle: "✱ Афоризми", Blueprint: "grouped-vertical",
			UnknownBucket: new("Інші"), Buckets: bucketsTwo("Класика", "Сучасні"),
		}},
		"hub-routed": {stanza: config.Stanza{
			Tag: "library/poetry", IndexTitle: "✱ Поезія", Blueprint: "hub-routed",
			UnknownBucket: new("Невідомі"), HubH2Prefix: new("Поеми"),
		}},
		"hub-routed-with-subtag": {stanza: config.Stanza{
			Tag: "personal/claude", IndexTitle: "✱ Claude", Blueprint: "hub-routed-with-subtag",
			UnknownBucket: new("_misc"), Buckets: bucketsTwo("session", "skill"),
		}},
		"umbrella": {
			stanza: config.Stanza{
				Tag: "library", IndexTitle: "✱ Library", Blueprint: "umbrella",
				Children:     sliceStrPtr("library/poetry"),
				DefaultChild: new("library/poetry"),
			},
			resolver: func(_ []string) ([]*domain.Domain, error) {
				return []*domain.Domain{libraryPoetry}, nil
			},
		},
	}
}

// TestDispatchEveryBlueprintBuilds: every blueprint string maps to a
// builder that produces a *domain.Domain whose primitive fields agree
// with the input stanza.
func TestDispatchEveryBlueprintBuilds(t *testing.T) {
	for blueprint, tc := range validDispatchCases() {
		t.Run(blueprint, func(t *testing.T) {
			assertDispatchBuildsCleanly(t, tc.stanza, tc.resolver)
		})
	}
}

// assertDispatchBuildsCleanly is the shared per-blueprint assertion.
// Helper extraction collapses the test's cognitive complexity below
// the project's gocognit ≤15 budget.
func assertDispatchBuildsCleanly(t *testing.T, stanza config.Stanza, resolver func([]string) ([]*domain.Domain, error)) {
	t.Helper()
	d, err := config.Dispatch(stanza, resolver)
	if err != nil {
		t.Fatalf("Dispatch(%s): unexpected error: %v", stanza.Blueprint, err)
	}
	if d == nil {
		t.Fatalf("Dispatch(%s): nil Domain", stanza.Blueprint)
	}
	if d.Tag != stanza.Tag {
		t.Errorf("Domain.Tag = %q, want %q", d.Tag, stanza.Tag)
	}
	if d.IndexTitle != stanza.IndexTitle {
		t.Errorf("Domain.IndexTitle = %q, want %q", d.IndexTitle, stanza.IndexTitle)
	}
	if d.CanonicalTag == "" {
		t.Errorf("Domain.CanonicalTag empty; expected derived from Tag")
	}
}

// TestDispatchGroupedVerticalSelectsVariantByTagDepth pins the one branch
// buildGroupedVertical adds: tag depth selects the canonicalization variant.
// A top-level tag gets the sub-tag variant (CanonicalTagFor emits
// `#tag/bucket`); an already-2-level tag gets the flat variant (CanonicalTagFor
// nil — bucket lives in the canonical 3rd segment). Without this assertion,
// flipping the depth check, dropping it, or hardcoding one factory leaves the
// rest of the suite green: assertDispatchBuildsCleanly checks only Tag /
// IndexTitle / CanonicalTag, none of which differ between the two factories.
func TestDispatchGroupedVerticalSelectsVariantByTagDepth(t *testing.T) {
	topLevel := config.Stanza{
		Tag: "reading", IndexTitle: "✱ Reading", Blueprint: "grouped-vertical",
		UnknownBucket: new("Other"), Buckets: sliceStrPtr("Books", "Talks"),
	}
	d, err := config.Dispatch(topLevel, noResolver())
	if err != nil {
		t.Fatalf("Dispatch(top-level grouped-vertical): %v", err)
	}
	if d.CanonicalTagFor == nil {
		t.Fatal("top-level grouped-vertical: CanonicalTagFor nil; want the sub-tag variant")
	}
	if got := d.CanonicalTagFor(d, "Books"); got != "#reading/Books" {
		t.Errorf("top-level grouped-vertical canonical = %q, want %q (sub-tag variant)", got, "#reading/Books")
	}

	twoLevel := config.Stanza{
		Tag: "library/aphorisms", IndexTitle: "✱ Афоризми", Blueprint: "grouped-vertical",
		UnknownBucket: new("Інші"), Buckets: sliceStrPtr("Класика", "Сучасні"),
	}
	d2, err := config.Dispatch(twoLevel, noResolver())
	if err != nil {
		t.Fatalf("Dispatch(2-level grouped-vertical): %v", err)
	}
	if d2.CanonicalTagFor != nil {
		t.Error("2-level grouped-vertical: CanonicalTagFor non-nil; want the flat variant (bucket in canonical 3rd segment)")
	}
}

// TestDispatchUnknownBlueprintSentinel: unknown blueprint produces an
// error wrapping ErrUnknownBlueprint with the valid catalog enumerated
// in the message body.
func TestDispatchUnknownBlueprintSentinel(t *testing.T) {
	stanza := config.Stanza{
		Tag: "x/y", IndexTitle: "X", Blueprint: "fancy",
	}
	_, err := config.Dispatch(stanza, noResolver())
	if err == nil {
		t.Fatal("Dispatch(unknown blueprint): want error, got nil")
	}
	if !errors.Is(err, config.ErrUnknownBlueprint) {
		t.Errorf("err = %v, want errors.Is ErrUnknownBlueprint", err)
	}
	for _, want := range []string{
		"flat-list", "grouped-vertical",
		"hub-routed", "hub-routed-with-subtag", "umbrella",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err message missing %q (helps users copy-paste valid value); got %q", want, err.Error())
		}
	}
}

// TestDispatchBlueprintFieldMismatch: flat-list with buckets set is a
// blueprint/field mismatch; dispatcher must reject with a message that
// names the offending field and the blueprint.
func TestDispatchBlueprintFieldMismatch(t *testing.T) {
	stanza := config.Stanza{
		Tag:        "llm/characters",
		IndexTitle: "✱ Characters",
		Blueprint:  "flat-list",
		Buckets:    sliceStrPtr("a", "b"), // not allowed for flat-list
	}
	_, err := config.Dispatch(stanza, noResolver())
	if err == nil {
		t.Fatal("Dispatch(flat-list with buckets): want error, got nil")
	}
	message := err.Error()
	if !strings.Contains(message, "flat-list") {
		t.Errorf("err missing 'flat-list': %q", message)
	}
	if !strings.Contains(message, "buckets") {
		t.Errorf("err missing 'buckets': %q", message)
	}
}

// TestDispatchUmbrellaResolvesChildren: umbrella delegates child
// resolution to the loader-supplied callback; resolved Domains must
// pass through to render.NewUmbrellaDomain.
func TestDispatchUmbrellaResolvesChildren(t *testing.T) {
	child := &domain.Domain{
		Tag: "library/poetry", CanonicalTag: "#library/poetry",
		IndexTitle: "✱ Поезія",
	}
	stanza := config.Stanza{
		Tag: "library", IndexTitle: "✱ Library", Blueprint: "umbrella",
		Children:     sliceStrPtr("library/poetry"),
		DefaultChild: new("library/poetry"),
	}
	called := false
	resolver := func(tags []string) ([]*domain.Domain, error) {
		called = true
		if len(tags) != 1 || tags[0] != "library/poetry" {
			t.Errorf("resolver called with %v, want [library/poetry]", tags)
		}
		return []*domain.Domain{child}, nil
	}
	d, err := config.Dispatch(stanza, resolver)
	if err != nil {
		t.Fatalf("Dispatch(umbrella): %v", err)
	}
	if !called {
		t.Error("resolver was not called for umbrella stanza")
	}
	if d == nil || d.Tag != "library" {
		t.Errorf("umbrella Domain.Tag = %v, want library", d)
	}
}

// TestDispatchUmbrellaMissingResolver: dispatcher rejects if the
// loader forgets to wire a resolver. This is a programmer-error
// guard, not a user-facing case — the message reads like a bug
// report ("loader bug") so it's obvious where to look.
func TestDispatchUmbrellaMissingResolver(t *testing.T) {
	stanza := config.Stanza{
		Tag: "library", IndexTitle: "✱ Library", Blueprint: "umbrella",
		Children:     sliceStrPtr("library/poetry"),
		DefaultChild: new("library/poetry"),
	}
	_, err := config.Dispatch(stanza, nil)
	if err == nil {
		t.Fatal("Dispatch(umbrella, nil resolver): want error")
	}
	if !strings.Contains(err.Error(), "resolveChildren") {
		t.Errorf("err should mention 'resolveChildren': %q", err.Error())
	}
}

// TestDispatchPositionalSafetyPatternK: same-typed positional args
// (Tag and IndexTitle are both string) are wired by NAME inside each
// builder, never by accident-of-arg-order. Swap the values in the
// stanza and confirm the resulting Domain still has the values
// assigned correctly to NAMED fields, not crossed.
func TestDispatchPositionalSafetyPatternK(t *testing.T) {
	stanza := config.Stanza{
		Tag:        "TAGVALUE",
		IndexTitle: "INDEXVALUE",
		Blueprint:  "flat-list",
	}
	d, err := config.Dispatch(stanza, noResolver())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if d.Tag != "TAGVALUE" {
		t.Errorf("Tag = %q, want TAGVALUE ( positional swap)", d.Tag)
	}
	if d.IndexTitle != "INDEXVALUE" {
		t.Errorf("IndexTitle = %q, want INDEXVALUE ( positional swap)", d.IndexTitle)
	}
}

package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/config"
)

// sliceStrPtr keeps test fixture construction concise — pointer-typed
// optional []string fields make repetition unavoidable otherwise.
// Single-string fixtures use Go 1.26 `new(value)` directly at call sites.
func sliceStrPtr(in ...string) *[]string { return new(append([]string(nil), in...)) }

// noResolver returns a resolveChildren that never gets called. Use it
// for non-umbrella stanzas where the dispatcher must not invoke the
// resolver.
func noResolver() func([]string) ([]*bear.Domain, error) {
	return nil
}

// TestDispatchMapSize: the closed catalog has EXACTLY 6 declarative
// blueprints. Adding or removing one requires an explicit map edit
// plus a new builder; this test is the canary.
func TestDispatchMapSize(t *testing.T) {
	if got := config.DispatchSize(); got != 6 {
		t.Fatalf("dispatch map size = %d, want 6 (closed catalog)", got)
	}
}

// dispatchCase couples a stanza with the resolver the dispatcher needs
// for that blueprint (only umbrella populates resolver).
type dispatchCase struct {
	stanza   config.Stanza
	resolver func([]string) ([]*bear.Domain, error)
}

// validDispatchCases produces one minimal-but-valid case per
// blueprint. Extracted so the table is data, not control-flow — keeps
// the test body small and dodges the dupl flag that would otherwise
// fire on the per-blueprint struct literals.
func validDispatchCases() map[string]dispatchCase {
	libraryPoetry := &bear.Domain{
		Tag: "library/poetry", CanonicalTag: "#library/poetry",
		IndexTitle: "✱ Поезія",
	}
	bucketsTwo := func(a, b string) *[]string { return sliceStrPtr(a, b) }
	return map[string]dispatchCase{
		"flat-list": {stanza: config.Stanza{
			Tag: "llm/characters", IndexTitle: "✱ Characters", Blueprint: "flat-list",
		}},
		"flat-table": {stanza: config.Stanza{
			Tag: "library/aphorisms", IndexTitle: "✱ Афоризми", Blueprint: "flat-table",
			UnknownBucket: new("Інші"), Buckets: bucketsTwo("Класика", "Сучасні"),
		}},
		"grouped-vertical": {stanza: config.Stanza{
			Tag: "personal/work", IndexTitle: "✱ Work", Blueprint: "grouped-vertical",
			UnknownBucket: new("_misc"), Buckets: bucketsTwo("meeting", "review"),
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
			resolver: func(_ []string) ([]*bear.Domain, error) {
				return []*bear.Domain{libraryPoetry}, nil
			},
		},
	}
}

// TestDispatchEveryBlueprintBuilds: every blueprint string maps to a
// builder that produces a *bear.Domain whose primitive fields agree
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
func assertDispatchBuildsCleanly(t *testing.T, stanza config.Stanza, resolver func([]string) ([]*bear.Domain, error)) {
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
		"flat-list", "flat-table", "grouped-vertical",
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
	msg := err.Error()
	if !strings.Contains(msg, "flat-list") {
		t.Errorf("err missing 'flat-list': %q", msg)
	}
	if !strings.Contains(msg, "buckets") {
		t.Errorf("err missing 'buckets': %q", msg)
	}
}

// TestDispatchUmbrellaResolvesChildren: umbrella delegates child
// resolution to the loader-supplied callback; resolved Domains must
// pass through to bear.NewUmbrellaDomain.
func TestDispatchUmbrellaResolvesChildren(t *testing.T) {
	child := &bear.Domain{
		Tag: "library/poetry", CanonicalTag: "#library/poetry",
		IndexTitle: "✱ Поезія",
	}
	stanza := config.Stanza{
		Tag: "library", IndexTitle: "✱ Library", Blueprint: "umbrella",
		Children:     sliceStrPtr("library/poetry"),
		DefaultChild: new("library/poetry"),
	}
	called := false
	resolver := func(tags []string) ([]*bear.Domain, error) {
		called = true
		if len(tags) != 1 || tags[0] != "library/poetry" {
			t.Errorf("resolver called with %v, want [library/poetry]", tags)
		}
		return []*bear.Domain{child}, nil
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

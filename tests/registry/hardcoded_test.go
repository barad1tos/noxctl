package registry_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/registry"
)

// TestHardcodedRegistryLength is the regression lock for the closed
// catalog of 27 leaves + 4 umbrellas (the recurring-pitfalls catalog).
// If anyone adds or removes a domain literal without updating
// MIGRATION.md and the equivalence-test fixture, this test screams.
func TestHardcodedRegistryLength(t *testing.T) {
	if got := len(registry.Hardcoded()); got != 27 {
		t.Errorf("Hardcoded(): got %d leaves, want 27", got)
	}
	if got := len(registry.HardcodedUmbrellas()); got != 4 {
		t.Errorf("HardcodedUmbrellas(): got %d umbrellas, want 4", got)
	}
	if got := len(registry.All()); got != 31 {
		t.Errorf("All(): got %d total, want 31", got)
	}
}

// TestHardcodedRegistryNoDuplicateTags catches accidental copy-paste
// collisions in Hardcoded (two entries with the same Tag would
// silently mask one in the equivalence test).
func TestHardcodedRegistryNoDuplicateTags(t *testing.T) {
	seen := make(map[string]int, 31)
	for _, d := range registry.All() {
		seen[d.Tag]++
	}
	for tag, n := range seen {
		if n != 1 {
			t.Errorf("tag %q appears %d times, want 1", tag, n)
		}
	}
}

// TestHardcodedRegistryUmbrellasStampParentMaster verifies the
// side-effect contract: NewUmbrellaDomain mutates each child's
// ParentMaster to the umbrella's IndexTitle. cmd/regen-watchd/main.go
// has always relied on this — the registry preserves it.
//
// Note: only leaves whose Tag has the umbrella's Tag as its first
// path segment are children. Bare-tag personal leaves (claude,
// english, work,...) are NOT children of any umbrella because
// there is no "personal" umbrella in the canonical registry.
func TestHardcodedRegistryUmbrellasStampParentMaster(t *testing.T) {
	all := registry.All()
	leaves := all[:27]
	umbrellas := all[27:]

	byTag := make(map[string]string, len(leaves))
	for _, l := range leaves {
		byTag[l.Tag] = l.ParentMaster
	}

	expected := map[string]string{}
	for _, u := range umbrellas {
		prefix := u.Tag + "/"
		for _, l := range leaves {
			if strings.HasPrefix(l.Tag, prefix) {
				expected[l.Tag] = u.IndexTitle
			}
		}
	}
	if len(expected) == 0 {
		t.Fatalf("no leaves matched any umbrella prefix; check umbrella Tags")
	}
	for tag, want := range expected {
		if got := byTag[tag]; got != want {
			t.Errorf("leaf %q ParentMaster: got %q, want %q", tag, got, want)
		}
	}
}

// TestHardcodedRegistryAllPreservesOrder locks the canonical order so
// the equivalence test can rely on positional indices.
//
// Order matches cmd/regen-watchd/main.go's `domains` slice verbatim:
// 6 library leaves, 4 llm leaves, 3 it leaves, 9 personal leaves
// (bare tags), 5 quicknote leaves, then 4 umbrellas in main.go
// order (it, library, llm, quicknote).
func TestHardcodedRegistryAllPreservesOrder(t *testing.T) {
	want := []string{
		// 0..5 — library (Poetry, Aphorisms, Articles, Lyrics, Prose, Quotes)
		"library/poetry", "library/aphorisms", "library/articles",
		"library/lyrics", "library/prose", "library/quotes",
		// 6..9 — llm
		"llm/agents", "llm/characters", "llm/rules", "llm/tips",
		// 10..12 — it
		"it/domains", "it/vendors", "it/technologies",
		// 13..21 — personal (bare tags)
		"claude", "english", "work",
		"health", "leisure", "humor",
		"instagram", "travel", "development",
		// 22..26 — quicknote
		"quicknote/daily", "quicknote/weekly", "quicknote/monthly",
		"quicknote/yearly", "quicknote/decadal",
		// 27..30 — umbrellas (main.go order: it, library, llm, quicknote)
		"it", "library", "llm", "quicknote",
	}
	got := make([]string, 0, 31)
	for _, d := range registry.All() {
		got = append(got, d.Tag)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("registry.All() order:\n got: %v\nwant: %v", got, want)
	}
}

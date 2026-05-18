package config_test

import (
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/registry"
)

// TestRomanCorpusPrimitiveEquivalence is 's acceptance gate
// for SPEC.md criterion 6 (28-domain corpus serializes faithfully).
//
// Loads examples/roman.toml via config.Load, then for every Tag in
// the hardcoded registry mirrored from cmd/regen-watchd/main.go,
// asserts primitive-field equality vs the loaded *bear.Domain.
//
// The custom-renderer trio (lyrics, quotes, agents) is asserted on
// PRIMITIVES only — RenderMaster identity is renderer-pointer
// comparison which is non-equatable for closure values, AND custom
// renderer behavior is 's TestDomainEquivalence concern
// (RESEARCH option (a) locked).
//
// Deferred to (renderer-identity assertion):
// - ParseMeta, RenderMaster, RenderHub, BacklinkFor, SectionFor,
// CanonicalTagFor, IsHubNote, HubTitleFor, BucketFromHubTitle,
// ParseMasterTable, SkipNote — these are function-typed fields,
// equality is structural-by-behavior, not pointer.
func TestRomanCorpusPrimitiveEquivalence(t *testing.T) {
	loaded, cat, err := config.Load(romanFixturePath(t))
	if err != nil {
		t.Fatalf("Load examples/roman.toml: %v", err)
	}
	if cat == nil {
		t.Fatalf("Load returned nil Catalog")
	}

	loadedByTag := make(map[string]*bear.Domain, len(loaded))
	for _, d := range loaded {
		if _, dup := loadedByTag[d.Tag]; dup {
			t.Errorf("loaded corpus has duplicate tag %q", d.Tag)
		}
		loadedByTag[d.Tag] = d
	}

	registry := hardcodedRegistry()

	if len(loadedByTag) != len(registry) {
		t.Errorf("count mismatch: loaded=%d hardcoded=%d", len(loadedByTag), len(registry))
		surfaceMissingTags(t, loadedByTag, registry)
	}

	for _, tag := range sortedKeys(registry) {
		hard := registry[tag]
		t.Run(tag, func(t *testing.T) {
			got, ok := loadedByTag[tag]
			if !ok {
				t.Fatalf("tag %q present in hardcoded registry but missing in loaded corpus", tag)
			}
			assertPrimitiveEquivalence(t, got, hard)
		})
	}
}

// hardcodedRegistry returns tag → existing *bear.Domain literal.
// Sources the slice from registry.All so the test never drifts
// from cmd/regen-watchd/main.go's canonical order. Umbrella
// construction lives inside registry.HardcodedUmbrellas — calling
// registry.All reproduces the same NewUmbrellaDomain side-effect
// (ParentMaster stamp on children) that main.go produces during init.
func hardcodedRegistry() map[string]*bear.Domain {
	all := registry.All()
	m := make(map[string]*bear.Domain, len(all))
	for _, d := range all {
		m[d.Tag] = d
	}
	return m
}

// assertPrimitiveEquivalence checks every Domain primitive field
// the dispatch map populates: Tag, IndexTitle, CanonicalTag,
// UnknownBucket, OwnGroup, OwnAliases (set equality), HubH2Prefix,
// HubH2Legacy (ordered slice equality — positional semantics matter
// for the legacy H2 fallback walk in bear/core.go::firstNonSectionH2),
// LegacyAuthorFallback, StripLegacyAuthorH2, SkipAtomicsPass. Each
// mismatch surfaces as its own t.Errorf so a single sub-test can list
// every offender for a clean CI diff (don't t.Fatalf after the first
// miss).
func assertPrimitiveEquivalence(t *testing.T, got, hard *bear.Domain) {
	t.Helper()
	if got.Tag != hard.Tag {
		t.Errorf("Tag: got %q want %q", got.Tag, hard.Tag)
	}
	if got.IndexTitle != hard.IndexTitle {
		t.Errorf("IndexTitle: got %q want %q", got.IndexTitle, hard.IndexTitle)
	}
	if got.CanonicalTag != hard.CanonicalTag {
		t.Errorf("CanonicalTag: got %q want %q", got.CanonicalTag, hard.CanonicalTag)
	}
	if got.UnknownBucket != hard.UnknownBucket {
		t.Errorf("UnknownBucket: got %q want %q", got.UnknownBucket, hard.UnknownBucket)
	}
	if got.OwnGroup != hard.OwnGroup {
		t.Errorf("OwnGroup: got %q want %q", got.OwnGroup, hard.OwnGroup)
	}
	if got.HubH2Prefix != hard.HubH2Prefix {
		t.Errorf("HubH2Prefix: got %q want %q", got.HubH2Prefix, hard.HubH2Prefix)
	}
	if got.LegacyAuthorFallback != hard.LegacyAuthorFallback {
		t.Errorf("LegacyAuthorFallback: got %v want %v",
			got.LegacyAuthorFallback, hard.LegacyAuthorFallback)
	}
	if got.StripLegacyAuthorH2 != hard.StripLegacyAuthorH2 {
		t.Errorf("StripLegacyAuthorH2: got %v want %v",
			got.StripLegacyAuthorH2, hard.StripLegacyAuthorH2)
	}
	if got.SkipAtomicsPass != hard.SkipAtomicsPass {
		t.Errorf("SkipAtomicsPass: got %v want %v",
			got.SkipAtomicsPass, hard.SkipAtomicsPass)
	}
	if !slices.Equal(got.HubH2Legacy, hard.HubH2Legacy) {
		t.Errorf("HubH2Legacy: got %v want %v",
			got.HubH2Legacy, hard.HubH2Legacy)
	}
	assertOwnAliasesEqual(t, got.OwnAliases, hard.OwnAliases)
}

// assertOwnAliasesEqual compares two map[string]struct{} sets by
// length + key membership. Empty / nil maps compare equal.
func assertOwnAliasesEqual(t *testing.T, got, hard map[string]struct{}) {
	t.Helper()
	if len(got) != len(hard) {
		t.Errorf("OwnAliases len: got %d want %d (got_keys=%v want_keys=%v)",
			len(got), len(hard), sortedSetKeys(got), sortedSetKeys(hard))
		return
	}
	for k := range hard {
		if _, ok := got[k]; !ok {
			t.Errorf("OwnAliases missing key %q", k)
		}
	}
}

// surfaceMissingTags lists tags only-in-loaded and only-in-hardcoded
// so a count-mismatch points at the offender immediately, no manual
// diff-walk required.
func surfaceMissingTags(t *testing.T,
	loaded map[string]*bear.Domain, hardcoded map[string]*bear.Domain,
) {
	t.Helper()
	for tag := range loaded {
		if _, ok := hardcoded[tag]; !ok {
			t.Errorf("only in loaded corpus (TOML): %q", tag)
		}
	}
	for tag := range hardcoded {
		if _, ok := loaded[tag]; !ok {
			t.Errorf("only in hardcoded registry (Go): %q", tag)
		}
	}
}

// romanFixturePath returns the absolute path to examples/roman.toml
// from the test working directory (tests/bear/config/). Three levels
// up reaches the repo root.
func romanFixturePath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "..", "examples", "roman.toml"))
	if err != nil {
		t.Fatalf("filepath.Abs roman.toml: %v", err)
	}
	return abs
}

// sortedKeys returns the registry's tags in alphabetical order so
// sub-test execution is deterministic and the test failure log is
// stable across runs.
func sortedKeys(m map[string]*bear.Domain) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedSetKeys returns a map[string]struct{}'s keys in alphabetical
// order, used for stable diff messages on OwnAliases mismatches.
func sortedSetKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

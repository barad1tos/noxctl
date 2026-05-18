// Package bear_test — equivalence_test.go (D-05 acceptance gate).
//
// TestDomainEquivalence pairs every of registry.All's 31 hardcoded
// *bear.Domain values against its TOML-loaded counterpart from
// examples/roman.toml and asserts byte-equal RenderMaster output on a
// per-blueprint synthetic fixture.
//
// Mirrors and extends tests/bear/config/roman_corpus_test.go::
// TestRomanCorpusPrimitiveEquivalence — that test asserts STRUCT
// FIELDS; this asserts RENDERED OUTPUT, which is what catches
// function-typed callback divergence (ParseMeta / RenderMaster /
// custom Apply identity).
//
// Hermetic — no bearcli, no Roman-vault dependency. Synthetic fixtures
// live under tests/bear/testdata/equivalence/<blueprint>.json. Helpers
// projectRoot(t) and firstDiffOffset(a, b []byte) are shared with
// codegen_test.go in the same bear_test package.
package bear_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/registry"
)

// TestDomainEquivalence is the MIGRATE-02 acceptance gate.
// Each sub-test runs one tag through both code paths (hardcoded
// *bear.Domain literal vs config.Load(examples/roman.toml) → Dispatch)
// and asserts byte-equal master render on a per-blueprint fixture.
//
// Reports both directions of mismatch (registry → TOML, TOML → registry)
// so a missing or stray tag surfaces as a top-level failure, not a
// silently-skipped sub-test.
func TestDomainEquivalence(t *testing.T) {
	t.Parallel()

	cfgPath := filepath.Join(projectRoot(t), "examples", "roman.toml")
	loaded, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load(%s): %v", cfgPath, err)
	}

	hardSlice := registry.All()
	tomlByTag := indexByTag(loaded)
	hardByTag := indexByTag(hardSlice)

	for _, hard := range hardSlice {
		tag := hard.Tag
		toml, ok := tomlByTag[tag]
		if !ok {
			t.Errorf("tag %q present in registry but missing from TOML", tag)
			continue
		}
		t.Run(tag, func(t *testing.T) {
			t.Parallel()
			fixture := loadFixtureForTag(t, tag, hard)
			compareRenderOutput(t, hard, toml, fixture)
		})
	}

	// Catch the inverse: TOML stanzas without a registry counterpart.
	for tag := range tomlByTag {
		if _, ok := hardByTag[tag]; !ok {
			t.Errorf("tag %q present in TOML but missing from registry", tag)
		}
	}
}

// compareRenderOutput renders the master via both Domains over the
// SAME fixture (each side groups via its own ParseMeta — the test
// asserts the resulting RenderMaster output is byte-equal).
// diagnostic: failure prints the offset of the first divergent byte
// plus a 500-char window from each side.
func compareRenderOutput(t *testing.T, hard, toml *bear.Domain, fixture []bear.Note) {
	t.Helper()

	hardOut := renderMaster(hard, fixture)
	tomlOut := renderMaster(toml, fixture)

	if bytes.Equal([]byte(hardOut), []byte(tomlOut)) {
		return
	}
	offset := firstDiffOffset([]byte(hardOut), []byte(tomlOut))
	t.Errorf("master render diverges for tag %q at byte offset %d\n"+
		"=== hardcoded (%d bytes) ===\n%s\n"+
		"=== toml (%d bytes) ===\n%s",
		hard.Tag, offset,
		len(hardOut), truncateForDiagnostic(hardOut, 500),
		len(tomlOut), truncateForDiagnostic(tomlOut, 500))
}

// renderMaster builds the per-bucket groups via the Domain's own
// ParseMeta and then asks RenderMaster for the master body. Mirrors
// what bear/snapshot.go::SnapshotDomainRenderInputs does for the
// production daemon — minus the bearcli round-trip, which the test
// owns the input for.
func renderMaster(d *bear.Domain, notes []bear.Note) string {
	groups := buildGroupsViaDomain(d, notes)
	return d.RenderMaster(d, groups)
}

// buildGroupsViaDomain runs d.ParseMeta over each fixture note and
// groups by Bucket. Empty bucket → d.UnknownBucket (or "" when
// UnknownBucket itself is empty — defensive against odd domain
// shapes; production ones always populate it).
func buildGroupsViaDomain(d *bear.Domain, notes []bear.Note) map[string][]bear.Note {
	groups := make(map[string][]bear.Note, 4)
	for _, n := range notes {
		var bucket string
		if d.ParseMeta != nil {
			bucket = d.ParseMeta(d, n.Content).Bucket
		}
		if bucket == "" {
			bucket = d.UnknownBucket
		}
		groups[bucket] = append(groups[bucket], n)
	}
	return groups
}

// indexByTag returns a Tag → *bear.Domain map for O(1) pairing
// between the two slices.
func indexByTag(ds []*bear.Domain) map[string]*bear.Domain {
	out := make(map[string]*bear.Domain, len(ds))
	for _, d := range ds {
		out[d.Tag] = d
	}
	return out
}

// truncateForDiagnostic keeps log output readable on failure. Adds a
// trailing ellipsis marker so it's obvious the body was cut short.
func truncateForDiagnostic(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...<truncated>"
}

// loadFixtureForTag resolves tag → blueprint via blueprintFileFor, then
// reads and JSON-decodes the matching fixture file under
// tests/bear/testdata/equivalence/.
func loadFixtureForTag(t *testing.T, tag string, d *bear.Domain) []bear.Note {
	t.Helper()
	fixtureFile := blueprintFileFor(tag, d)
	path := filepath.Join(projectRoot(t), "tests", "bear", "testdata", "equivalence", fixtureFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s for tag %q: %v", path, tag, err)
	}
	var notes []bear.Note
	if err = json.Unmarshal(raw, &notes); err != nil {
		t.Fatalf("parse fixture %s for tag %q: %v", path, tag, err)
	}
	if len(notes) == 0 {
		t.Fatalf("fixture %s for tag %q is empty", path, tag)
	}
	return notes
}

// blueprintFileFor maps a domain to its fixture filename via the
// per-blueprint heuristic from 04-RESEARCH.md Pattern 5.
//
// Resolution order matters: the three custom-renderer tags (lyrics /
// quotes / agents) are pinned first because the corresponding
// *bear.Domain shapes are otherwise indistinguishable from a normal
// hub-routed Domain (NewHubRoutedDomain base + custom Apply on top).
// Umbrella check (SkipAtomicsPass) wins next, then HubH2Prefix splits
// hub-routed-vs-subtag via CanonicalTagFor presence. Finally an
// explicit flat-table list (since NewGroupedVerticalFlatDomain doesn't
// set CanonicalTagFor whereas NewGroupedVerticalDomain does — but both
// use ParseMetaFlatTable / ParseMetaFromSubTag respectively, and the
// test's grouping doesn't notice the distinction once UnknownBucket
// catches every fixture note).
func blueprintFileFor(tag string, d *bear.Domain) string {
	customs := map[string]string{
		"library/lyrics": "custom-lyrics.json",
		"library/quotes": "custom-quotes.json",
		"llm/agents":     "custom-agents.json",
	}
	if f, ok := customs[tag]; ok {
		return f
	}
	if d.SkipAtomicsPass {
		return "umbrella.json"
	}
	if d.HubH2Prefix != "" {
		if d.CanonicalTagFor != nil {
			return "hub-routed-subtag.json"
		}
		return "hub-routed.json"
	}
	flatTable := map[string]bool{
		"library/aphorisms": true,
		"library/prose":     true,
		"it/vendors":        true,
		"it/technologies":   true,
	}
	if flatTable[tag] {
		return "flat-table.json"
	}
	if d.CanonicalTagFor != nil {
		return "grouped-vertical.json"
	}
	return "flat-list.json"
}

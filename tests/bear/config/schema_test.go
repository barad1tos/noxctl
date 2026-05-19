// Package config_test exercises bear/config schema decoding round-trips
// and sentinel-error wiring.
//
// Tests live in the external test package so they catch import-path
// regressions and reflect what downstream callers (cmd/noxctl/) see.
package config_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/barad1tos/noxctl/bear/config"
)

// TestSchemaRoundTripValidMinimal: valid-minimal.toml decodes cleanly,
// every key is consumed (no metadata.Undecoded drift), and the
// expected primitive fields are populated.
func TestSchemaRoundTripValidMinimal(t *testing.T) {
	raw := mustReadFixture(t, "valid-minimal.toml")
	var cat config.Catalog
	meta, err := toml.Decode(raw, &cat)
	if err != nil {
		t.Fatalf("toml.Decode(valid-minimal): %v", err)
	}
	if undec := meta.Undecoded(); len(undec) != 0 {
		t.Fatalf("valid-minimal: unexpected undecoded keys: %v", undec)
	}
	if cat.Meta.Version != "1" {
		t.Errorf("Meta.Version = %q, want %q", cat.Meta.Version, "1")
	}
	if cat.Meta.Locale != "uk" {
		t.Errorf("Meta.Locale = %q, want %q", cat.Meta.Locale, "uk")
	}
	if len(cat.Domains) != 1 {
		t.Fatalf("len(Domains) = %d, want 1", len(cat.Domains))
	}
	d := cat.Domains[0]
	if d.Tag != "library/poetry" {
		t.Errorf("Tag = %q, want library/poetry", d.Tag)
	}
	if d.IndexTitle != "✱ Поезія" {
		t.Errorf("IndexTitle = %q, want ✱ Поезія", d.IndexTitle)
	}
	if d.Blueprint != "hub-routed" {
		t.Errorf("Blueprint = %q, want hub-routed", d.Blueprint)
	}
	if d.OwnGroup == nil || *d.OwnGroup != "Моя поезія" {
		t.Errorf("OwnGroup = %v, want pointer to %q", d.OwnGroup, "Моя поезія")
	}
	if d.UnknownBucket == nil || *d.UnknownBucket != "Невідомі" {
		t.Errorf("UnknownBucket = %v, want pointer to %q", d.UnknownBucket, "Невідомі")
	}
	if d.HubH2Prefix == nil || *d.HubH2Prefix != "Поеми" {
		t.Errorf("HubH2Prefix = %v, want pointer to %q", d.HubH2Prefix, "Поеми")
	}
}

// TestSchemaRoundTripAllBlueprints: every blueprint shape decodes
// cleanly, irrelevant pointer fields stay nil, relevant ones populate.
func TestSchemaRoundTripAllBlueprints(t *testing.T) {
	raw := mustReadFixture(t, "valid-all-blueprints.toml")
	var cat config.Catalog
	meta, err := toml.Decode(raw, &cat)
	if err != nil {
		t.Fatalf("toml.Decode(valid-all-blueprints): %v", err)
	}
	if undec := meta.Undecoded(); len(undec) != 0 {
		t.Fatalf("valid-all-blueprints: unexpected undecoded keys: %v", undec)
	}
	if got := len(cat.Domains); got != 6 {
		t.Fatalf("len(Domains) = %d, want 6 (one per blueprint)", got)
	}
	byBlueprint := indexByBlueprint(cat.Domains)
	assertEveryBlueprintPresent(t, byBlueprint)
	assertFlatListShape(t, byBlueprint["flat-list"])
	assertFlatTableShape(t, byBlueprint["flat-table"])
	assertHubRoutedShape(t, byBlueprint["hub-routed"])
	assertUmbrellaShape(t, byBlueprint["umbrella"])
}

func indexByBlueprint(stanzas []config.Stanza) map[string]config.Stanza {
	out := make(map[string]config.Stanza, len(stanzas))
	for _, s := range stanzas {
		out[s.Blueprint] = s
	}
	return out
}

func assertEveryBlueprintPresent(t *testing.T, byBlueprint map[string]config.Stanza) {
	t.Helper()
	for _, bp := range []string{
		"flat-list", "flat-table", "grouped-vertical",
		"hub-routed", "hub-routed-with-subtag", "umbrella",
	} {
		if _, ok := byBlueprint[bp]; !ok {
			t.Errorf("missing blueprint %q in fixture", bp)
		}
	}
}

// assertFlatListShape: only required fields populated; pointer-typed
// optionals nil. Catches any future schema drift that adds default
// values where the fixture omits them.
func assertFlatListShape(t *testing.T, fl config.Stanza) {
	t.Helper()
	if fl.Buckets != nil || fl.OwnGroup != nil || fl.HubH2Prefix != nil ||
		fl.UnknownBucket != nil || fl.Children != nil {
		t.Errorf("flat-list: expected nil pointers for optionals, got %+v", fl)
	}
}

func assertFlatTableShape(t *testing.T, ft config.Stanza) {
	t.Helper()
	if ft.Buckets == nil || len(*ft.Buckets) != 2 {
		t.Errorf("flat-table: Buckets = %v, want 2 entries", ft.Buckets)
	}
	if ft.UnknownBucket == nil || *ft.UnknownBucket != "Інші" {
		t.Errorf("flat-table: UnknownBucket = %v, want %q", ft.UnknownBucket, "Інші")
	}
	if ft.HubH2Prefix != nil || ft.OwnGroup != nil || ft.Children != nil {
		t.Errorf("flat-table: expected nil hub_h2_prefix/own_group/children, got %+v", ft)
	}
}

func assertHubRoutedShape(t *testing.T, hr config.Stanza) {
	t.Helper()
	if hr.OwnGroup == nil || *hr.OwnGroup != "Моя поезія" {
		t.Errorf("hub-routed: OwnGroup = %v, want Моя поезія", hr.OwnGroup)
	}
	if hr.OwnAliases == nil || len(*hr.OwnAliases) != 2 {
		t.Errorf("hub-routed: OwnAliases = %v, want 2 entries", hr.OwnAliases)
	}
	if hr.LegacyAuthorFallback == nil || !*hr.LegacyAuthorFallback {
		t.Errorf("hub-routed: LegacyAuthorFallback = %v, want pointer-true", hr.LegacyAuthorFallback)
	}
	if hr.StripLegacyAuthorH2 == nil || !*hr.StripLegacyAuthorH2 {
		t.Errorf("hub-routed: StripLegacyAuthorH2 = %v, want pointer-true", hr.StripLegacyAuthorH2)
	}
}

func assertUmbrellaShape(t *testing.T, um config.Stanza) {
	t.Helper()
	switch {
	case um.Children == nil || len(*um.Children) != 2:
		t.Errorf("umbrella: Children = %v, want 2 entries", um.Children)
	case um.DefaultChild == nil || *um.DefaultChild != "library/poetry":
		t.Errorf("umbrella: DefaultChild = %v, want pointer to %q", um.DefaultChild, "library/poetry")
	case um.Buckets != nil, um.OwnGroup != nil, um.HubH2Prefix != nil:
		t.Errorf("umbrella: expected nil buckets/own_group/hub_h2_prefix, got %+v", um)
	}
}

// TestSchemaSnakeCaseEnforced: replacing index_title with indexTitle
// (camelCase) makes BurntSushi flag the camelCase key as undecoded —
// confirms snake_case tags are load-bearing.
func TestSchemaSnakeCaseEnforced(t *testing.T) {
	src := `
[meta]
version = "1"
locale  = "uk"

[[domain]]
tag         = "x/y"
indexTitle  = "Camel"
blueprint   = "flat-list"
`
	var cat config.Catalog
	meta, err := toml.Decode(src, &cat)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cat.Domains[0].IndexTitle != "" {
		t.Errorf("IndexTitle should NOT decode from camelCase key; got %q", cat.Domains[0].IndexTitle)
	}
	found := false
	for _, k := range meta.Undecoded() {
		if strings.Contains(k.String(), "indexTitle") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected indexTitle in metadata.Undecoded(); got %v", meta.Undecoded())
	}
}

// TestSchemaSingularDomainTable: confirms [[domain]] (singular) is
// the canonical form; [[domains]] (plural) decodes to nothing useful
// and shows up in metadata.Undecoded.
func TestSchemaSingularDomainTable(t *testing.T) {
	src := `
[meta]
version = "1"
locale  = "uk"

[[domains]]
tag         = "x/y"
index_title = "Plural"
blueprint   = "flat-list"
`
	var cat config.Catalog
	meta, err := toml.Decode(src, &cat)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cat.Domains) != 0 {
		t.Errorf("plural [[domains]] should not populate Domains slice; got %d entries", len(cat.Domains))
	}
	found := false
	for _, k := range meta.Undecoded() {
		if strings.HasPrefix(k.String(), "domains") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'domains' (plural) in metadata.Undecoded(); got %v", meta.Undecoded())
	}
}

// TestSentinelErrUnknownBlueprint: errors.Is recognizes wrapped sentinel.
func TestSentinelErrUnknownBlueprint(t *testing.T) {
	wrapped := fmt.Errorf("dispatch %q: %w", "fancy", config.ErrUnknownBlueprint)
	if !errors.Is(wrapped, config.ErrUnknownBlueprint) {
		t.Errorf("errors.Is(wrap, ErrUnknownBlueprint) = false, want true")
	}
	if errors.Is(wrapped, config.ErrSchemaVersion) {
		t.Errorf("errors.Is(unknown-blueprint wrap, ErrSchemaVersion) = true, want false")
	}
}

// TestSentinelErrSchemaVersion: same chain semantics for version.
func TestSentinelErrSchemaVersion(t *testing.T) {
	wrapped := fmt.Errorf("path: %w: meta.version", config.ErrSchemaVersion)
	if !errors.Is(wrapped, config.ErrSchemaVersion) {
		t.Errorf("errors.Is(wrap, ErrSchemaVersion) = false, want true")
	}
}

// TestSentinelErrDuplicateTag: same chain semantics for duplicates.
func TestSentinelErrDuplicateTag(t *testing.T) {
	wrapped := fmt.Errorf("path: %w: tag %q", config.ErrDuplicateTag, "library/poetry")
	if !errors.Is(wrapped, config.ErrDuplicateTag) {
		t.Errorf("errors.Is(wrap, ErrDuplicateTag) = false, want true")
	}
}

// mustReadFixture reads a testdata fixture or fails the test.
func mustReadFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return string(raw)
}

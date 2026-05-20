package config_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/config"
)

// fixturePath returns the absolute path to a testdata fixture so the
// loader's path-anchored error messages stay deterministic across
// test runs (vs working-dir-relative).
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("filepath.Abs %s: %v", name, err)
	}
	return abs
}

// TestValidateCatalogVersionStrict: only "1" passes; any other string,
// empty string, or float-shaped "1.0" fails with ErrSchemaVersion.
func TestValidateCatalogVersionStrict(t *testing.T) {
	cases := []struct {
		name      string
		version   string
		wantValid bool
	}{
		{"v1-string-passes", "1", true},
		{"v2-string-fails", "2", false},
		{"empty-fails", "", false},
		{"float-string-fails", "1.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertVersionValidation(t, tc.version, tc.wantValid)
		})
	}
}

// assertVersionValidation runs ValidateCatalog against a stanza-free
// catalog with the given version and asserts the expected outcome.
// Helper extraction collapses the test's gocognit budget.
func assertVersionValidation(t *testing.T, version string, wantValid bool) {
	t.Helper()
	cat := &config.Catalog{Meta: config.Meta{Version: version, Locale: "uk"}}
	err := config.ValidateCatalog(cat, "test.toml")
	if wantValid {
		if err != nil {
			t.Errorf("Version=%q: expected valid, got %v", version, err)
		}
		return
	}
	if err == nil {
		t.Fatalf("Version=%q: expected error, got nil", version)
	}
	if !errors.Is(err, config.ErrSchemaVersion) {
		t.Errorf("Version=%q: expected errors.Is ErrSchemaVersion, got %v", version, err)
	}
}

// TestValidateCatalogLocaleWhitelist: v1 ships uk only.
func TestValidateCatalogLocaleWhitelist(t *testing.T) {
	cat := &config.Catalog{
		Meta: config.Meta{Version: "1", Locale: "en"},
	}
	err := config.ValidateCatalog(cat, "test.toml")
	if err == nil {
		t.Fatal("locale=en: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "locale") {
		t.Errorf("err should mention 'locale': %q", err.Error())
	}
}

// TestValidateCatalogDuplicateTag: two stanzas sharing a tag fail
// with ErrDuplicateTag and both indices appear in the message.
func TestValidateCatalogDuplicateTag(t *testing.T) {
	cat := &config.Catalog{
		Meta: config.Meta{Version: "1", Locale: "uk"},
		Domains: []config.Stanza{
			{Tag: "library/poetry", IndexTitle: "P1", Blueprint: "flat-list"},
			{Tag: "library/poetry", IndexTitle: "P2", Blueprint: "flat-list"},
		},
	}
	err := config.ValidateCatalog(cat, "test.toml")
	if err == nil {
		t.Fatal("duplicate tags: expected error")
	}
	if !errors.Is(err, config.ErrDuplicateTag) {
		t.Errorf("expected errors.Is ErrDuplicateTag, got %v", err)
	}
}

// TestValidateCatalogThreeLevelTag: tag tree depth limit (max 2
// segments per the sidebar-flatness rule).
func TestValidateCatalogThreeLevelTag(t *testing.T) {
	err := validateOneStanzaTag(t, "library/poetry/biko")
	if err == nil {
		t.Fatal("three-level tag: expected error")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("err should mention 'depth': %q", err.Error())
	}
}

// TestValidateCatalogTagInjection: tag with shell metacharacters
// rejected by the security regex.
func TestValidateCatalogTagInjection(t *testing.T) {
	err := validateOneStanzaTag(t, "foo;rm -rf /")
	if err == nil {
		t.Fatal("tag injection: expected error")
	}
	if !strings.Contains(err.Error(), "match") || !strings.Contains(err.Error(), "tag") {
		t.Errorf("err should mention regex/tag: %q", err.Error())
	}
}

// validatePromotionsCat builds a minimal v=1 catalog with the given
// promotion block and runs ValidateCatalog against it. The catalog
// declares two synthetic flat-list domains ("a" and "b") so the
// promotion-target validator has known tags to resolve against —
// without these stanzas the new unreachable-`to` check would taint
// otherwise-clean test cases. Shared by every "promotion-shape"
// assertion to keep the body under the dupl threshold.
func validatePromotionsCat(t *testing.T, promos []config.Promotion) error {
	t.Helper()
	cat := &config.Catalog{
		Meta: config.Meta{Version: "1", Locale: "uk"},
		Domains: []config.Stanza{
			{Tag: "a", IndexTitle: "A", Blueprint: "flat-list"},
			{Tag: "b", IndexTitle: "B", Blueprint: "flat-list"},
		},
		Promotions: promos,
	}
	return config.ValidateCatalog(cat, "test.toml")
}

// assertPromotionsRejected runs validatePromotionsCat against the
// given promotion table and asserts the aggregated error mentions
// every required substring. Centralizes the rejection-shape boilerplate
// so the per-defect tests stay under the dupl threshold.
func assertPromotionsRejected(t *testing.T, label string, promos []config.Promotion, mustContain ...string) {
	t.Helper()
	err := validatePromotionsCat(t, promos)
	if err == nil {
		t.Fatalf("%s: expected error, got nil", label)
	}
	for _, want := range mustContain {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err should mention %q: %q", label, want, err.Error())
		}
	}
}

// TestValidatePromotions_UnknownBoundary: catalog with a [[promotion]]
// block carrying a typo in `boundary` fails at load with the accepted
// set in the message. Without this guard the rules-driven promoter
// would silently treat the rule as a no-op.
func TestValidatePromotions_UnknownBoundary(t *testing.T) {
	assertPromotionsRejected(t, "unknown boundary",
		[]config.Promotion{{From: "a", To: "b", Boundary: "fortnight"}},
		"unknown boundary", "day|week|month|year")
}

// TestValidatePromotions_DuplicateFrom: two [[promotion]] blocks with
// the same `from` tag fail loudly. Otherwise the catalog-driven index
// would keep only one rule and the operator would never see the rest
// of their ladder applied.
func TestValidatePromotions_DuplicateFrom(t *testing.T) {
	assertPromotionsRejected(t, "duplicate from",
		[]config.Promotion{
			{From: "inbox", To: "week", Boundary: "day"},
			{From: "inbox", To: "year", Boundary: "month"},
		},
		"duplicate")
}

// TestValidatePromotions_EmptyBoundaryAccepted: empty boundary defaults
// to "day" — the most common operator case writes no explicit value.
// Guards against a regression where the validator gets stricter than
// the runtime and rejects the documented shorthand.
func TestValidatePromotions_EmptyBoundaryAccepted(t *testing.T) {
	if err := validatePromotionsCat(t, []config.Promotion{
		{From: "a", To: "b", Boundary: ""},
	}); err != nil {
		t.Errorf("empty boundary: expected no error, got %v", err)
	}
}

// TestValidatePromotions_RejectedShapes pins every shape the
// validator must catch at load time so the runtime never hits the
// matching footgun. Each row is one rule plus the substring the
// aggregated error must mention.
//
//   - self-loop (From == To): would hang PromoteByCalendar's chain
//     loop because the boundary check stays satisfied forever.
//   - unknown target: typo'd `to` (e.g. "weeky") points at a tag no
//     domain owns and no rule chains from — promoted notes silently
//     disappear from the regen pipeline.
func TestValidatePromotions_RejectedShapes(t *testing.T) {
	cases := []struct {
		label string
		rule  config.Promotion
		want  string
	}{
		{"self-loop", config.Promotion{From: "a", To: "a", Boundary: "day"}, "self-loop"},
		{
			"unknown target",
			config.Promotion{From: "a", To: "weeky", Boundary: "day"},
			"does not match any declared domain",
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			assertPromotionsRejected(t, tc.label, []config.Promotion{tc.rule}, tc.want)
		})
	}
}

// TestValidatePromotions_ChainedFromCountsAsTarget: a multi-step
// ladder is valid when the target tag is either a declared domain or
// another rule's `from`. Here "b → orphan" is reachable because the
// next rule's `from = "orphan"` claims it, even though "orphan" is
// not a declared domain. Pins the chain-aware target check so future
// tightening doesn't break legitimate ladders.
func TestValidatePromotions_ChainedFromCountsAsTarget(t *testing.T) {
	err := validatePromotionsCat(t, []config.Promotion{
		{From: "a", To: "orphan", Boundary: "day"},
		{From: "orphan", To: "b", Boundary: "week"},
	})
	if err != nil {
		t.Errorf("chained ladder: expected no error, got %v", err)
	}
}

// validateOneStanzaTag builds a minimal v=1 catalog with a single
// flat-list stanza carrying the given tag and runs ValidateCatalog
// against it. Shared by every "single-stanza tag-shape" assertion.
func validateOneStanzaTag(t *testing.T, tag string) error {
	t.Helper()
	cat := &config.Catalog{
		Meta: config.Meta{Version: "1", Locale: "uk"},
		Domains: []config.Stanza{
			{Tag: tag, IndexTitle: "X", Blueprint: "flat-list"},
		},
	}
	return config.ValidateCatalog(cat, "test.toml")
}

// TestValidateCatalogBucketBearURL: unknown_bucket containing bear://
// callback URL is a tampering surface — reject.
func TestValidateCatalogBucketBearURL(t *testing.T) {
	cat := &config.Catalog{
		Meta: config.Meta{Version: "1", Locale: "uk"},
		Domains: []config.Stanza{
			{Tag: "x/y", IndexTitle: "X", Blueprint: "flat-list", UnknownBucket: new("bear://x-callback-url/foo")},
		},
	}
	err := config.ValidateCatalog(cat, "test.toml")
	if err == nil {
		t.Fatal("bear:// bucket: expected error")
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("err should mention 'forbidden': %q", err.Error())
	}
}

// TestLoaderFileNotFound: missing file produces an error wrapping
// fs.ErrNotExist.
func TestLoaderFileNotFound(t *testing.T) {
	missing := "/nonexistent/path/noxctl.toml.does-not-exist"
	_, _, err := config.Load(missing)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected errors.Is fs.ErrNotExist, got %v", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("err should include path %q: %v", missing, err)
	}
}

// TestLoaderUnknownFieldWithLineLoc: typo'd field surfaces in the
// aggregate error message — exact line:col formatting depends on
// metadata.Undecoded granularity.
func TestLoaderUnknownFieldWithLineLoc(t *testing.T) {
	path := fixturePath(t, "broken-typo.toml")
	_, _, err := config.Load(path)
	if err == nil {
		t.Fatal("broken-typo: expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "tags") {
		t.Errorf("err should name offending key 'tags': %q", msg)
	}
	if !strings.Contains(msg, path) {
		t.Errorf("err should include path %q: %q", path, msg)
	}
}

// TestLoaderUnknownBlueprint: blueprint = "fancy" surfaces wrapped
// ErrUnknownBlueprint with the valid catalog enumerated.
func TestLoaderUnknownBlueprint(t *testing.T) {
	path := fixturePath(t, "broken-blueprint.toml")
	_, _, err := config.Load(path)
	if err == nil {
		t.Fatal("broken-blueprint: expected error")
	}
	if !errors.Is(err, config.ErrUnknownBlueprint) {
		t.Errorf("expected errors.Is ErrUnknownBlueprint, got %v", err)
	}
	for _, want := range []string{"flat-list", "umbrella"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err should list valid blueprint %q: %q", want, err.Error())
		}
	}
}

// TestLoaderVersionIntRejected: version = 1 (int) is a TOML type
// mismatch against Meta.Version's string type. BurntSushi's error
// message contains "version" and "type" tokens, so we check for
// substring presence rather than ParseError typing — the int/string
// type-mismatch path emits a non-ParseError sentinel internally.
func TestLoaderVersionIntRejected(t *testing.T) {
	path := fixturePath(t, "broken-version-int.toml")
	_, _, err := config.Load(path)
	if err == nil {
		t.Fatal("broken-version-int: expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "version") {
		t.Errorf("err should mention 'version': %q", msg)
	}
	if !strings.Contains(msg, "type") && !strings.Contains(msg, "string") {
		t.Errorf("err should mention type-mismatch (type/string): %q", msg)
	}
}

// TestLoaderAggregatesMultipleErrors: a fixture with three
// independent problems surfaces all three substrings in one
// errors.Join aggregate.
func TestLoaderAggregatesMultipleErrors(t *testing.T) {
	path := fixturePath(t, "broken-multiple.toml")
	_, _, err := config.Load(path)
	if err == nil {
		t.Fatal("broken-multiple: expected aggregate error")
	}
	msg := err.Error()
	// 1. Top-level [bogus] table flagged as undecoded
	if !strings.Contains(msg, "bogus") {
		t.Errorf("err missing 'bogus' (top-level undecoded): %q", msg)
	}
	// 2. blueprint = "fancy" flagged as unknown
	if !strings.Contains(msg, "fancy") {
		t.Errorf("err missing 'fancy' (unknown blueprint): %q", msg)
	}
	// 3. duplicate tag flagged
	if !errors.Is(err, config.ErrDuplicateTag) {
		t.Errorf("err should wrap ErrDuplicateTag for the duplicate stanza pair: %q", msg)
	}
}

// TestLoaderDuplicateTag: dedicated fixture confirms the
// duplicate-tag path in isolation (TestLoaderAggregatesMultipleErrors
// also covers it but bundles other errors).
func TestLoaderDuplicateTag(t *testing.T) {
	path := fixturePath(t, "broken-duplicate-tag.toml")
	_, _, err := config.Load(path)
	if err == nil {
		t.Fatal("broken-duplicate-tag: expected error")
	}
	if !errors.Is(err, config.ErrDuplicateTag) {
		t.Errorf("expected errors.Is ErrDuplicateTag, got %v", err)
	}
}

// TestLoaderThreeLevelTag: validator depth check fires from inside Load.
func TestLoaderThreeLevelTag(t *testing.T) {
	assertLoadFailureContains(t, "broken-three-level-tag.toml", "depth")
}

// TestLoaderTagInjection: validator regex check fires from inside Load.
func TestLoaderTagInjection(t *testing.T) {
	assertLoadFailureContains(t, "broken-tag-injection.toml", "match")
}

// assertLoadFailureContains loads the named fixture, asserts the load
// fails, and confirms the error message includes the given substring.
// Shared by tests that exercise a single ValidateCatalog trigger from
// inside the loader.
func assertLoadFailureContains(t *testing.T, fixture, want string) {
	t.Helper()
	path := fixturePath(t, fixture)
	_, _, err := config.Load(path)
	if err == nil {
		t.Fatalf("%s: expected error", fixture)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("%s err should mention %q: %q", fixture, want, err.Error())
	}
}

// TestLoaderValidAllBlueprintsHappyPath: full corpus loads cleanly,
// returns 6 *domain.Domain, no error.
func TestLoaderValidAllBlueprintsHappyPath(t *testing.T) {
	path := fixturePath(t, "valid-all-blueprints.toml")
	domains, cat, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load(valid-all-blueprints): unexpected error: %v", err)
	}
	if cat == nil {
		t.Fatal("Catalog should not be nil on success")
	}
	if len(domains) != 6 {
		t.Errorf("expected 6 Domains, got %d", len(domains))
	}
	for i, d := range domains {
		if d == nil {
			t.Errorf("domain[%d] is nil", i)
			continue
		}
		if d.Tag == "" {
			t.Errorf("domain[%d].Tag empty", i)
		}
	}
}

// TestLoaderPerf: 28-stanza fixture parses in well under the 1-second
// budget. Sanity-checks the loader function in isolation; the
// user-facing wall-clock against examples/personal.toml is measured
// separately at the CLI level.
func TestLoaderPerf(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "perf.toml")
	makePerfFixture(t, path, 28)
	start := time.Now()
	_, _, err := config.Load(path)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("perf fixture load: %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("Load took %v, expected < 200ms for 28 flat-list stanzas (budget %v cold)", elapsed, time.Second)
	}
}

func makePerfFixture(t *testing.T, path string, count int) {
	t.Helper()
	var b strings.Builder
	b.WriteString("[meta]\nversion = \"1\"\nlocale = \"uk\"\n\n")
	for i := range count {
		b.WriteString("[[domain]]\n")
		b.WriteString("tag         = \"perf/stanza-")
		b.WriteString(itoa(i))
		b.WriteString("\"\n")
		b.WriteString("index_title = \"Stanza ")
		b.WriteString(itoa(i))
		b.WriteString("\"\n")
		b.WriteString("blueprint   = \"flat-list\"\n\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write perf fixture: %v", err)
	}
}

// itoa is a strconv-free integer formatter — keeps the perf fixture
// generator self-contained without dragging strconv into the import
// list above (strconv is fine to use; we just don't need it elsewhere).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestLoaderZeroBearcli: static guarantee — bear/config/ MUST NOT
// import bearcli helpers, because Load runs without spawning any
// process. Verified by reading the source files of the package
// itself.
func TestLoaderZeroBearcli(t *testing.T) {
	root := configSrcDir(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", root, err)
	}
	forbidden := []string{"runBearcli", "/Applications/Bear", "os/exec", "exec.Command"}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("ReadFile %s: %v", path, readErr)
		}
		text := string(raw)
		for _, bad := range forbidden {
			if strings.Contains(text, bad) {
				t.Errorf("%s contains forbidden token %q (zero-bearcli guarantee)", path, bad)
			}
		}
	}
}

// configSrcDir resolves bear/config/ relative to this test file.
func configSrcDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	// wd is.../tests/bear/config; bear/config/ is up three then down two.
	root := filepath.Clean(filepath.Join(wd, "..", "..", "..", "bear", "config"))
	return root
}

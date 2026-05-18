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
// segments per / Open Q5).
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
// fs.ErrNotExist — VAL-01 acceptance criterion.
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
// errors.Join aggregate (D-11).
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
// returns 6 *bear.Domain, no error.
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
// budget. is the abstraction's first perf gate (D-17 measures
// the user-facing wall-clock against examples/roman.toml in;
// here we sanity-check the loader function in isolation).
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
// process (VAL-02). Verified by reading the source files of the
// package itself.
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
				t.Errorf("%s contains forbidden token %q (zero-bearcli guarantee VAL-02)", path, bad)
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

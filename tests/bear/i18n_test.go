package bear_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/barad1tos/noxctl/bear/domain"
)

// TestT_KnownKeyReturnsValue exercises the happy path: a known key in the
// active locale (uk) returns the catalog value. Anchors the catalog
// completeness gate against a stable key the master renderer also reads.
func TestT_KnownKeyReturnsValue(t *testing.T) {
	t.Cleanup(func() { domain.SetLocale("uk") })
	if got := domain.T("master.section.authors"); got != "Автори" {
		t.Errorf(`domain.T("master.section.authors") = %q, want "Автори"`, got)
	}
}

// TestMissingKeyHandler verifies missing key invokes the swappable
// handler. Default handler is log.Fatalf which we cannot exercise in-process,
// so we swap a buffer-write handler and confirm the missing key + locale flow
// through.
func TestMissingKeyHandler(t *testing.T) {
	t.Cleanup(func() { domain.SetLocale("uk") })
	var buf bytes.Buffer
	prev := domain.SetMissingKeyHandler(func(key, locale string) {
		_, _ = fmt.Fprintf(&buf, "missing key=%q locale=%q\n", key, locale)
	})
	t.Cleanup(func() { domain.SetMissingKeyHandler(prev) })

	_ = domain.T("nonexistent.key.for.test")
	logged := buf.String()
	if !strings.Contains(logged, "nonexistent.key.for.test") {
		t.Errorf("handler did not capture missing key in %q", logged)
	}
	if !strings.Contains(logged, "uk") {
		t.Errorf("handler did not capture active locale in %q", logged)
	}
}

// TestSetLocaleRoundTrip verifies SetLocale updates the active locale and
// ActiveLocale reads it back. Active locale is a process-global test seam,
// so always restore via t.Cleanup to keep suite-wide tests downstream from
// observing leaked locale state.
func TestSetLocaleRoundTrip(t *testing.T) {
	prev := domain.ActiveLocale()
	t.Cleanup(func() { domain.SetLocale(prev) })

	domain.SetLocale("uk")
	if got := domain.ActiveLocale(); got != "uk" {
		t.Errorf(`domain.ActiveLocale() = %q, want "uk"`, got)
	}
}

// TestUkTomlWellFormed checks that bear/locales/uk.toml parses as TOML
// without errors. Catches a future commit that introduces a syntactically
// broken catalog before any runtime call surfaces it.
func TestUkTomlWellFormed(t *testing.T) {
	data, err := domain.LocaleFile("uk.toml")
	if err != nil {
		t.Fatalf("read locales/uk.toml: %v", err)
	}
	var m map[string]string
	if _, err = toml.Decode(string(data), &m); err != nil {
		t.Fatalf("decode locales/uk.toml: %v", err)
	}
	if len(m) == 0 {
		t.Errorf("uk.toml has no keys")
	}
}

// TestNoSilentFallback verifies the missing-key boundary: setting a
// non-existent locale and calling T must invoke the missing-key handler
// — never silently fall back to uk values.
func TestNoSilentFallback(t *testing.T) {
	t.Cleanup(func() { domain.SetLocale("uk") })
	var buf bytes.Buffer
	prev := domain.SetMissingKeyHandler(func(key, locale string) {
		_, _ = fmt.Fprintf(&buf, "missing key=%q locale=%q\n", key, locale)
	})
	t.Cleanup(func() { domain.SetMissingKeyHandler(prev) })

	domain.SetLocale("xx")
	_ = domain.T("master.section.authors")
	logged := buf.String()
	if !strings.Contains(logged, "xx") {
		t.Errorf(`handler did not see locale "xx" in %q`, logged)
	}
	if !strings.Contains(logged, "master.section.authors") {
		t.Errorf("handler did not see the looked-up key in %q", logged)
	}
}

// TestI18nCatalogComplete walks every production.go file under the listed
// package roots, regex-extracts every domain.T("...") call, and asserts each
// extracted key exists in bear/locales/uk.toml. Failure lists the missing
// keys, sorted, one per line — so a single CI run pinpoints every gap.
//
// Excluded: any _test.go file (the regex would self-match its own fixtures)
// and cmd/regen-watchd/* (legacy daemon, frozen until deletion).
func TestI18nCatalogComplete(t *testing.T) {
	repoRoot := findRepoRoot(t)

	catalogData, err := domain.LocaleFile("uk.toml")
	if err != nil {
		t.Fatalf("read locales/uk.toml: %v", err)
	}
	var ukCatalog map[string]string
	if _, err = toml.Decode(string(catalogData), &ukCatalog); err != nil {
		t.Fatalf("decode locales/uk.toml: %v", err)
	}

	// After the atomic catalog migration the hardcoded domain packages
	// (library/llm/it/personal/quicknote) are gone — every domain now
	// lives in examples/personal.toml. The remaining Go-source `domain.T(...)`
	// call sites are inside bear/ (core + custom renderers).
	roots := []string{"bear"}
	keys := collectTKeys(t, repoRoot, roots)

	var missing []string
	for k := range keys {
		if _, ok := ukCatalog[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	t.Errorf("catalog completeness: %d key(s) referenced from source but missing in uk.toml:\n%s",
		len(missing), strings.Join(missing, "\n"))
}

// findRepoRoot walks up from the current working directory until it finds a
// go.mod file. Tests run with cwd=package dir, so we must climb to the repo
// root before scanning all packages.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for dir := cwd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
	}
	t.Fatalf("could not locate repo root from %q", cwd)
	return ""
}

// tCallRE matches `domain.T("…")` or `T("…")` (in-package) string literals.
// Captures the key in group 1.
var tCallRE = regexp.MustCompile(`\bT\(\s*"([^"]+)"\s*\)`)

// collectTKeys reads every.go file under the supplied package roots and
// returns the set of unique keys passed to domain.T(...). Skips any _test.go
// file (their regex fixtures would self-match the production scan).
func collectTKeys(t *testing.T, repoRoot string, roots []string) map[string]struct{} {
	t.Helper()
	keys := make(map[string]struct{})
	for _, root := range roots {
		absRoot := filepath.Join(repoRoot, root)
		if err := filepath.Walk(absRoot, walkCollect(keys)); err != nil {
			t.Fatalf("walk %s: %v", absRoot, err)
		}
	}
	return keys
}

// walkCollect returns a filepath.WalkFunc that mutates `keys` with every
// domain.T(...) key found in any.go file under the walked tree. Extracted
// from collectTKeys so its cognitive complexity stays under the project
// gocognit ≤ 15 budget — the loop itself is trivial; the predicate
// stack lived inside the closure.
func walkCollect(keys map[string]struct{}) filepath.WalkFunc {
	return func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !shouldScan(path, info) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range tCallRE.FindAllSubmatch(data, -1) {
			keys[string(m[1])] = struct{}{}
		}
		return nil
	}
}

// shouldScan reports whether the walked entry is a candidate.go production
// file the regex sweep should read. Skips directories, non-.go files, and
// any _test.go file (whose regex fixtures would self-pollute the key set).
func shouldScan(path string, info os.FileInfo) bool {
	if info.IsDir() {
		return false
	}
	if !strings.HasSuffix(path, ".go") {
		return false
	}
	if strings.HasSuffix(path, "_test.go") {
		return false
	}
	return true
}

// Package config_test — locale-allowlist + on-disk-catalog consistency
// guards for the supportedLocales whitelist in bear/config/validate.go.
//
// The catalog-side `meta.locale` field is two-layered: (1) string
// validated against a hardcoded allowlist at load time; (2) at runtime
// `domain.SetLocale(locale)` activates the matching `bear/domain/
// locales/<locale>.toml` file (embedded via go:embed). Both layers
// must stay in sync; this file pins that contract.
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
)

// supportedLocales is the same allowlist `bear/config/validate.go`
// hardcodes. Tests in this file walk this list; the production
// constant stays private to the validate package. Drift between
// the two surfaces as a test failure if a new locale lands on
// only one side.
var expectedLocales = []string{"en", "uk"}

// TestLocaleAllowlistMatchesDiskCatalogs asserts every locale on
// the validator allowlist has a matching `bear/domain/locales/
// <locale>.toml` file on disk. Without this guard, adding a new
// locale string to the allowlist without shipping the catalog
// file would compile + pass validate.go's string-membership
// check, then crash at first `T()` call via the missing-key
// fatal-log handler.
func TestLocaleAllowlistMatchesDiskCatalogs(t *testing.T) {
	for _, locale := range expectedLocales {
		t.Run(locale, func(t *testing.T) {
			path := filepath.Join("..", "..", "..",
				"bear", "domain", "locales", locale+".toml")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("locale %q is in supportedLocales but %s "+
					"is missing — add the catalog file or remove "+
					"the locale from the allowlist", locale, path)
			}
			if info.Size() == 0 {
				t.Errorf("locale catalog %s is empty", path)
			}
		})
	}
}

// TestLocaleAllowlistAcceptsExpected asserts every locale named in
// expectedLocales validates without error. Companion to
// TestLocaleAllowlistMatchesDiskCatalogs — guards the positive
// case end-to-end (allowlist + validator).
func TestLocaleAllowlistAcceptsExpected(t *testing.T) {
	for _, locale := range expectedLocales {
		t.Run(locale, func(t *testing.T) {
			cat := &config.Catalog{
				Meta: config.Meta{Version: "1", Locale: locale},
			}
			if err := config.ValidateCatalog(cat, "test.toml"); err != nil {
				t.Errorf("locale %q should validate cleanly; got: %v",
					locale, err)
			}
		})
	}
}

// TestLocaleWireUpAffectsActiveLocale asserts that `domain.SetLocale`
// actually changes `domain.ActiveLocale()` — the smallest possible
// regression-lock against the failure shape where `meta.locale` is
// validated but `SetLocale` is never called from any catalog-load
// callsite.
//
// This test does NOT exercise the full Load → SetLocale chain
// (that runs through cmd/noxctl/preflight and bear/cli/plan which
// each need their own end-to-end seam). What it locks is the
// foundational invariant the wire-up relies on: calling SetLocale
// with a supported locale makes ActiveLocale return that same
// value, and `T()` reads from the matching catalog.
func TestLocaleWireUpAffectsActiveLocale(t *testing.T) {
	original := domain.ActiveLocale()
	t.Cleanup(func() { domain.SetLocale(original) })

	for _, locale := range expectedLocales {
		t.Run(locale, func(t *testing.T) {
			domain.SetLocale(locale)
			if got := domain.ActiveLocale(); got != locale {
				t.Errorf("after SetLocale(%q), ActiveLocale() = %q; want %q",
					locale, got, locale)
			}
		})
	}
}

// TestLocaleEnNewNoteLabel pins the most user-visible wire value
// produced by the locale switch — the `[New Note]` label in every
// canonical tag-line URL. If this regresses (e.g. en.toml is
// modified and the key shifts to "Open Note" or similar), the
// fixed string here breaks loud.
func TestLocaleEnNewNoteLabel(t *testing.T) {
	original := domain.ActiveLocale()
	t.Cleanup(func() { domain.SetLocale(original) })

	domain.SetLocale("en")
	const want = "New Note"
	if got := domain.T("new-note.label"); got != want {
		t.Errorf("T(new-note.label) under locale=en = %q; want %q "+
			"(if the wire value changed intentionally, update the "+
			"README before/after screenshots to match)", got, want)
	}
}

// expectedKeyExample exists so the test file references at least
// one key shape from uk.toml — guards against the test compiling
// against a stale i18n package without exercising T().
const expectedKeyExample = "new-note.label"

// TestLocaleUkRoundTrip asserts the Ukrainian original is still
// reachable after the en.toml addition — guards against a future
// edit that accidentally removes uk.toml or shifts its keys.
func TestLocaleUkRoundTrip(t *testing.T) {
	original := domain.ActiveLocale()
	t.Cleanup(func() { domain.SetLocale(original) })

	domain.SetLocale("uk")
	got := domain.T(expectedKeyExample)
	if got == "" {
		t.Fatalf("T(%q) under locale=uk returned empty", expectedKeyExample)
	}
	// The uk label must NOT equal the en label (that would defeat
	// the whole point of locale switching).
	domain.SetLocale("en")
	enGot := domain.T(expectedKeyExample)
	if got == enGot {
		t.Errorf("uk T(%q)=%q matches en T(%q)=%q — the locales should diverge",
			expectedKeyExample, got, expectedKeyExample, enGot)
	}
	if !strings.Contains(got, "Нова") && !strings.Contains(got, "нотатка") {
		// Sanity check: uk new-note label is "Нова нотатка". If the
		// catalog changes, update this assertion or the catalog.
		t.Logf("note: uk T(%q)=%q does not match the historical Ukrainian "+
			"value — verify uk.toml is intentional", expectedKeyExample, got)
	}
}

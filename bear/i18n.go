// Package bear i18n — embedded localized string catalog.
//
// `bear.T(key)` is the single lookup point for user-visible strings.
// Catalogs are embedded at compile time via `//go:embed locales/*.toml`
// so a deployed binary always carries the strings it needs;
// future locales drop sibling.toml files with no build-shape change.
//
// Missing keys never silently fall back. The default handler is
// `log.Fatalf`, surfacing typos and missing entries as a fast startup
// failure. Tests swap `missingKeyHandler` for a buffer-write stub and
// restore via `t.Cleanup`.
//
// Init ordering note: `var FooDomain = &bear.Domain{...}` literals in
// sibling packages call `bear.T(...)` at package init time. Go's init
// order guarantees same-package init runs before cross-package var
// init, so this package's `init` populates the catalog before any
// `library/`, `llm/`, etc. var literal evaluates.
package bear

import (
	"embed"
	"io/fs"
	"log"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

//go:embed locales/*.toml
var localeFS embed.FS

var (
	catalogMu    sync.RWMutex
	catalog      = make(map[string]map[string]string) // locale → key → value
	activeLocale = "uk"

	// missingKeyHandler is invoked when T(key) finds no entry for the
	// active locale. Default fatally logs; tests swap for a buffer-write
	// stub via t.Cleanup. Package-level var (not const) so test code can
	// reach it without exporting the seam.
	missingKeyHandler = func(key, locale string) {
		log.Fatalf("i18n: missing key %q in locale %q", key, locale)
	}
)

func init() {
	entries, err := localeFS.ReadDir("locales")
	if err != nil {
		log.Fatalf("i18n: ReadDir locales: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		locale := strings.TrimSuffix(e.Name(), ".toml")
		data, readErr := fs.ReadFile(localeFS, "locales/"+e.Name())
		if readErr != nil {
			log.Fatalf("i18n: read %s: %v", e.Name(), readErr)
		}
		m := make(map[string]string)
		if _, decodeErr := toml.Decode(string(data), &m); decodeErr != nil {
			log.Fatalf("i18n: decode %s: %v", e.Name(), decodeErr)
		}
		catalog[locale] = m
	}
	if _, ok := catalog[activeLocale]; !ok {
		log.Fatalf("i18n: active locale %q has no catalog file", activeLocale)
	}
}

// T returns the localized string for key in the active locale. Missing
// keys invoke missingKeyHandler (default: log.Fatalf). Never silently
// falls back to a different locale.
func T(key string) string {
	catalogMu.RLock()
	m, ok := catalog[activeLocale]
	locale := activeLocale
	catalogMu.RUnlock()
	if ok {
		if v, hit := m[key]; hit {
			return v
		}
	}
	missingKeyHandler(key, locale)
	return ""
}

// SetLocale changes the active locale used by T. Tests must restore the
// prior value via t.Cleanup (— explicit per-test reset).
func SetLocale(locale string) {
	catalogMu.Lock()
	activeLocale = locale
	catalogMu.Unlock()
}

// ActiveLocale returns the currently selected locale.
func ActiveLocale() string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return activeLocale
}

// SetMissingKeyHandler swaps the missing-key handler and returns the prior
// one so callers can restore it. Test seam: the default handler is
// log.Fatalf, which tests cannot exercise in-process; tests swap a
// buffer-write stub via this entry point and restore via t.Cleanup.
// Production code never calls this.
func SetMissingKeyHandler(h func(key, locale string)) func(key, locale string) {
	prev := missingKeyHandler
	missingKeyHandler = h
	return prev
}

// LocaleFile returns the raw bytes of a file under the embedded locales/
// directory (e.g. LocaleFile("uk.toml")). Test seam for catalog-completeness
// gates that need to introspect the embedded TOML directly. Production
// callers should use T(key) instead.
func LocaleFile(name string) ([]byte, error) {
	return fs.ReadFile(localeFS, "locales/"+name)
}

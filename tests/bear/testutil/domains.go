// Package testutil exposes the single source of truth for *bear.Domain
// test fixtures. Tests in this repo MUST NOT import the
// library/llm/it/personal/quicknote constructor packages directly —
// resolve fixtures through Domain/Domains/LoadDomains here instead.
// examples/roman.toml is the canonical catalog the production daemon
// also loads.
//
// All helpers acceptb testing.TB and fail the test on any lookup miss,
// keeping the call sites short. The catalog is parsed once per
// process via sync.OnceValues; every accessor reuses the cached slice
// (no repeated TOML reads, every *bear.Domain pointer for a given tag
// is stable across calls so pointer-identity assertions stay
// deterministic).
package testutil

import (
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/config"
)

// catalogPathOnce resolves examples/roman.toml absolutely. The
// resolution is anchored to this file's location via runtime.Caller
// so tests run from any working directory.
var catalogPathOnce = sync.OnceValue(func() string {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		// runtime.Caller(0) failing is effectively impossible for a
		// compiled Go binary; panicking surfaces the issue at test
		// bootstrap instead of as a downstream "file not found" red
		// herring.
		panic("testutil: runtime.Caller(0) returned !ok")
	}
	// here = .../tests/bear/testutil/domains.go ; repo root is up 3.
	return filepath.Join(filepath.Dir(here), "..", "..", "..", "examples", "roman.toml")
})

// catalogOnce parses examples/roman.toml exactly once per process.
// Subsequent accessor calls reuse the cached slice — no repeated TOML
// I/O, every accessor for a given tag returns the same *bear.Domain
// pointer.
var catalogOnce = sync.OnceValues(func() ([]*bear.Domain, error) {
	domains, _, err := config.Load(catalogPathOnce())
	return domains, err
})

// CatalogPath returns the absolute path to examples/roman.toml,
// resolved from this file's location via runtime.Caller. Useful for
// tests that pass the path to a production loader (e.g. config.Load)
// rather than reusing the cached domain slice.
func CatalogPath(tb testing.TB) string {
	tb.Helper()
	return catalogPathOnce()
}

// LoadDomains returns the parsed domain slice from examples/roman.toml.
// Fails the test on parse error. Reuses the cached parse via
// sync.OnceValues.
func LoadDomains(tb testing.TB) []*bear.Domain {
	tb.Helper()
	domains, err := catalogOnce()
	if err != nil {
		tb.Fatalf("testutil.LoadDomains: %v", err)
	}
	return domains
}

// LoadCatalog is a backwards-compatible alias for LoadDomains. The
// underlying *config.Catalog is intentionally discarded — tests that
// need catalog metadata (Meta, Features) should call config.Load
// directly via the cached path constant.
func LoadCatalog(tb testing.TB) []*bear.Domain {
	return LoadDomains(tb)
}

// Domain returns the single domain whose Tag equals the supplied tag.
// Fails the test if no match exists. Returns the cached *bear.Domain
// pointer; repeated calls for the same tag are pointer-identical.
func Domain(tb testing.TB, tag string) *bear.Domain {
	tb.Helper()
	for _, d := range LoadDomains(tb) {
		if d.Tag == tag {
			return d
		}
	}
	tb.Fatalf("testutil.Domain(%q): not present in catalog", tag)
	return nil
}

// Domains returns the slice of domains matching the supplied tags, in
// the requested order. Fails the test if any tag is missing.
func Domains(tb testing.TB, tags ...string) []*bear.Domain {
	tb.Helper()
	cat := LoadDomains(tb)
	byTag := make(map[string]*bear.Domain, len(cat))
	for _, d := range cat {
		byTag[d.Tag] = d
	}
	out := make([]*bear.Domain, 0, len(tags))
	for _, tag := range tags {
		d, ok := byTag[tag]
		if !ok {
			tb.Fatalf("testutil.Domains: tag %q not present in catalog", tag)
		}
		out = append(out, d)
	}
	return out
}

// Command noxctl-codegen is the one-shot bridge tool that regenerates
// examples/roman.toml from the canonical hardcoded *bear.Domain slice
// (registry.All).
//
// D-08 commitment: this tool exists ONLY during the migration
// bridge window. It is deleted alongside library/, llm/, it/, personal/,
// quicknote/ and the registry/ package in the D-12 atomic deletion
// commit (plan 04-07).
//
// Mechanics:
//
// 1. Load the existing examples/roman.toml so meta/features/i18n and the
// per-domain `buckets` lists (which *bear.Domain doesn't carry as a
// field — buckets are a positional argument to factories like
// NewGroupedVerticalFlatDomain) propagate verbatim.
// 2. Walk registry.All and translate each *bear.Domain into a
// config.Stanza via domainToStanza (per-blueprint dispatch heuristic).
// 3. Emit the resulting *config.Catalog through toml.NewEncoder.
//
// Acceptance: byte-equal output round-trip. After step 3 writes the
// regenerated file, running the tool a second time produces an identical
// stream — the test in tests/bear/codegen_test.go locks that contract.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/registry"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "noxctl-codegen: %v\n", err)
		os.Exit(1)
	}
}

// run is the testable entry point — main forwards exit codes; run
// returns errors so a future in-process caller (or test) can drive the
// tool without subprocess overhead.
func run() error {
	out := flag.String("output", "-", "output path; - for stdout")
	source := flag.String("source", defaultSourcePath, "existing roman.toml to bootstrap meta/features/i18n/buckets from")
	flag.Parse()

	cat, err := buildCatalog(*source)
	if err != nil {
		return err
	}
	return emit(cat, *out)
}

// defaultSourcePath is the canonical Roman corpus location relative to
// the repo root. The tool is invoked from the repo root by `go run`
// during tests and developer cycles; falling back to this path keeps
// the smoke-run command line short.
const defaultSourcePath = "examples/roman.toml"

// buildCatalog loads sourcePath, harvests per-tag bucket lists from the
// existing stanzas, then re-emits one Stanza per registry.All entry.
// Meta + Features + I18N copy through verbatim — they're operator data
// that the tool has no opinion about.
func buildCatalog(sourcePath string) (*config.Catalog, error) {
	existing, err := loadExisting(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", sourcePath, err)
	}
	bucketsByTag := harvestBuckets(existing)

	domains := registry.All()
	stanzas := make([]config.Stanza, 0, len(domains))
	for _, d := range domains {
		s, stanzaErr := domainToStanza(d, bucketsByTag[d.Tag])
		if stanzaErr != nil {
			return nil, fmt.Errorf("domain %q: %w", d.Tag, stanzaErr)
		}
		stanzas = append(stanzas, s)
	}

	return &config.Catalog{
		Meta:     existing.Meta,
		Domains:  stanzas,
		Features: existing.Features,
		I18N:     existing.I18N,
	}, nil
}

// loadExisting decodes sourcePath via raw toml.DecodeFile so the meta /
// features / i18n / buckets blocks travel through unchanged. We
// deliberately bypass config.Load here — Load runs strict validation
// (Undecoded keys, blueprint validation, dispatch) which would reject
// a partially-regenerated intermediate state. Codegen needs the raw
// shape only.
func loadExisting(path string) (*config.Catalog, error) {
	var cat config.Catalog
	if _, err := toml.DecodeFile(path, &cat); err != nil {
		return nil, err
	}
	return &cat, nil
}

// harvestBuckets builds tag → buckets map from the existing Catalog.
// Domains whose Buckets pointer is nil (flat-list, hub-routed, umbrella)
// map to nil; the dispatch in domainToStanza checks per-blueprint.
func harvestBuckets(cat *config.Catalog) map[string][]string {
	out := make(map[string][]string, len(cat.Domains))
	for _, s := range cat.Domains {
		if s.Buckets != nil {
			cp := make([]string, len(*s.Buckets))
			copy(cp, *s.Buckets)
			out[s.Tag] = cp
		}
	}
	return out
}

// emit writes cat as a TOML document to outPath. "-" means stdout. The
// encoder default indentation matches BurntSushi v1.6.0 conventions; we
// don't override since the byte-equality acceptance test compares
// against the encoder's own output.
func emit(cat *config.Catalog, outPath string) error {
	if outPath == "-" {
		return toml.NewEncoder(os.Stdout).Encode(cat)
	}
	f, err := os.Create(outPath) //nolint:gosec // codegen is a developer tool; output path is operator-supplied via flag.
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer func() { _ = f.Close() }()
	if err = toml.NewEncoder(f).Encode(cat); err != nil {
		return fmt.Errorf("encode %s: %w", outPath, err)
	}
	return nil
}

// Package config — loader.
//
// Load is a single-pass strict decoder + dispatcher. Order of
// operations:
//
// 1. Read file → fs.ErrNotExist surfaces unwrapped.
// 2. toml.Decode → ParseError emits `path:line:col: msg`.
// Type-mismatch errors (e.g. `version = 1` int vs string field)
// do NOT route through ParseError; they surface as plain
// wrapped errors with the field path embedded.
// 3. metadata.Undecoded — flag every unknown key.
// 4. ValidateCatalog — catalog-level invariants.
// 5. Two-pass dispatch: leaf domains first, then umbrellas.
// 6. Domain.Validate per built domain.
//
// Errors aggregate via errors.Join. On any error path we still return
// the partially-built []*domain.Domain so tooling can introspect what
// DID parse cleanly.
package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/barad1tos/noxctl/bear/domain"
)

// Load parses noxctl.toml at path, dispatches stanzas, and returns
// ([]*domain.Domain, *Catalog, error). Error is errors.Join of every
// problem encountered — never just the first.
func Load(path string) ([]*domain.Domain, *Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		// Preserve fs.ErrNotExist via %w so callers can errors.Is
		// against it directly.
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}

	catalog, undecoded, decodeErr := decodeStrict(raw, path)
	if decodeErr != nil {
		return nil, nil, decodeErr
	}

	aggregated := append([]error(nil), undecoded...)
	if validateErr := ValidateCatalog(catalog, path); validateErr != nil {
		aggregated = append(aggregated, validateErr)
	}

	built, dispatchErrs := dispatchAllStanzas(catalog, path)
	aggregated = append(aggregated, dispatchErrs...)

	if len(aggregated) > 0 {
		return built, catalog, errors.Join(aggregated...)
	}
	return built, catalog, nil
}

// decodeStrict reads the TOML body, surfaces ParseError with explicit
// `path:line:col: msg` formatting, and returns the metadata-derived
// list of undecoded-key errors so the caller can join them with the
// rest of the aggregate. Return order keeps error last (revive
// error-return convention).
func decodeStrict(raw []byte, path string) (*Catalog, []error, error) {
	var catalog Catalog
	meta, err := toml.Decode(string(raw), &catalog)
	if err != nil {
		if pe, ok := errors.AsType[toml.ParseError](err); ok {
			return nil, nil, fmt.Errorf("%s:%d:%d: %s",
				path, pe.Position.Line, pe.Position.Col, pe.Error())
		}
		// Type-mismatch and other non-ParseError decode failures
		// (the int-vs-string version case) surface here. Wrap so
		// path is visible; preserve underlying err via %w.
		return nil, nil, fmt.Errorf("%s: decode: %w", path, err)
	}

	var undecoded []error
	for _, key := range meta.Undecoded() {
		undecoded = append(undecoded, fmt.Errorf("%s: unknown field %q", path, key.String()))
	}
	return &catalog, undecoded, nil
}

// dispatchAllStanzas runs the two-pass dispatch:
// - First pass: every non-umbrella stanza → domain.Domain via the
// dispatch map; results indexed by Tag for the second-pass
// resolver to consume.
// - Second pass: umbrella stanzas dispatched with a resolveChildren
// closure that looks up child Tags in the leaf map.
//
// Returns the slice of built domains (positionally aligned with
// catalog.Domains; failed dispatches leave nil placeholders) and the
// list of dispatch+validate errors.
func dispatchAllStanzas(catalog *Catalog, path string) ([]*domain.Domain, []error) {
	var errs []error
	leaf := map[string]*domain.Domain{}
	var umbrellas []int
	built := make([]*domain.Domain, len(catalog.Domains))

	for i, s := range catalog.Domains {
		if s.Blueprint == "umbrella" {
			umbrellas = append(umbrellas, i)
			continue
		}
		built[i] = dispatchOne(s, i, path, nil, &errs)
		if built[i] != nil {
			leaf[s.Tag] = built[i]
		}
	}

	if len(umbrellas) == 0 {
		return built, errs
	}
	resolver := newChildResolver(leaf)
	for _, i := range umbrellas {
		built[i] = dispatchOne(catalog.Domains[i], i, path, resolver, &errs)
	}
	return built, errs
}

// dispatchOne runs Dispatch + Domain.Validate for a single stanza,
// appending any error encountered to errs. Returns the *domain.Domain
// pointer so the caller can register leaf domains.
func dispatchOne(s Stanza, i int, path string,
	resolver func([]string) ([]*domain.Domain, error),
	errs *[]error,
) *domain.Domain {
	d, err := Dispatch(s, resolver)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: domain[%d] tag=%q: %w", path, i, s.Tag, err))
		return nil
	}
	if validateErr := d.Validate(); validateErr != nil {
		*errs = append(*errs, fmt.Errorf("%s: domain[%d] tag=%q validate: %w", path, i, s.Tag, validateErr))
		return nil
	}
	return d
}

// newChildResolver returns a resolveChildren closure that maps each
// child Tag to a previously-built leaf domain. Missing children are
// returned as a single error listing every offender.
func newChildResolver(leaf map[string]*domain.Domain) func([]string) ([]*domain.Domain, error) {
	return func(tags []string) ([]*domain.Domain, error) {
		kids := make([]*domain.Domain, 0, len(tags))
		var missing []string
		for _, t := range tags {
			if d, ok := leaf[t]; ok {
				kids = append(kids, d)
			} else {
				missing = append(missing, t)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("missing children: %v", missing)
		}
		return kids, nil
	}
}

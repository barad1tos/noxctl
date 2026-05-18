package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// tagFormatRE allows lowercase / uppercase letters, digits, slash,
// underscore and hyphen. Anything else is rejected as a tag-injection
// surface (T-1-14: `tag = "foo;rm -rf /"`). The regex anchors to the
// whole string so partial matches don't slip through.
var tagFormatRE = regexp.MustCompile(`^[a-zA-Z0-9_/-]+$`)

// bucketRejectREs lists patterns that must NOT appear in user-
// supplied bucket names. T-1-15 mitigates bear://x-callback-url
// injection that would otherwise produce arbitrary callback wikilinks
// when a reader clicks the master.
var bucketRejectREs = []*regexp.Regexp{
	regexp.MustCompile(`://`),
	regexp.MustCompile(`(?i)^bear:`),
}

// supportedLocales locks v1 to Ukrainian. Future locales must add
// both their key here AND ship a `bear/locales/<key>.toml` catalog
// (/ I18N-01). Empty locale is allowed — caller treats as
// "use default".
var supportedLocales = map[string]struct{}{"uk": {}}

// ValidateCatalog runs catalog-level invariants AFTER successful TOML
// decode and BEFORE per-stanza dispatch. Aggregates every problem
// into one errors.Join return — never short-circuits on first
// failure (D-11).
//
// path is included in every nested error so multi-file callers
// (rare; the loader currently calls with one path) get
// disambiguation for free.
func ValidateCatalog(cat *Catalog, path string) error {
	var errs []error
	errs = append(errs, validateMeta(cat, path)...)
	errs = append(errs, validateDomains(cat, path)...)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// validateMeta covers [meta].version + [meta].locale. Extracted to
// keep ValidateCatalog under the gocognit budget.
func validateMeta(cat *Catalog, path string) []error {
	var errs []error
	if cat.Meta.Version != "1" {
		errs = append(errs, fmt.Errorf("%s: %w: meta.version must be %q (got %q)",
			path, ErrSchemaVersion, "1", cat.Meta.Version))
	}
	if cat.Meta.Locale != "" {
		if _, ok := supportedLocales[cat.Meta.Locale]; !ok {
			errs = append(errs, fmt.Errorf(
				"%s: meta.locale %q unsupported (v1 supports: uk)",
				path, cat.Meta.Locale))
		}
	}
	return errs
}

// validateDomains walks every stanza, applying the per-stanza
// invariants and tracking duplicates. Returns the full list of
// problems; never short-circuits.
func validateDomains(cat *Catalog, path string) []error {
	var errs []error
	seen := map[string]int{}
	for i, d := range cat.Domains {
		errs = append(errs, validateStanzaInvariants(d, i, path)...)
		if d.Tag == "" {
			continue
		}
		if prev, dup := seen[d.Tag]; dup {
			errs = append(errs, fmt.Errorf("%s: %w: tag %q appears at domain[%d] and domain[%d]",
				path, ErrDuplicateTag, d.Tag, prev, i))
		} else {
			seen[d.Tag] = i
		}
	}
	return errs
}

// validateStanzaInvariants runs the per-stanza invariants that don't
// require neighbor-context (duplicate detection lives in
// validateDomains because it needs the full slice).
func validateStanzaInvariants(d Stanza, i int, path string) []error {
	var errs []error
	if d.Tag == "" {
		errs = append(errs, fmt.Errorf("%s: domain[%d]: tag is required", path, i))
	} else {
		if !tagFormatRE.MatchString(d.Tag) {
			errs = append(errs, fmt.Errorf(
				"%s: domain[%d] tag=%q: must match [a-zA-Z0-9_/-]+ (security: tag-injection guard)",
				path, i, d.Tag))
		}
		if strings.Count(d.Tag, "/") > 1 {
			errs = append(errs, fmt.Errorf(
				"%s: domain[%d] tag=%q: tag tree depth limit exceeded (max 2 segments per project )",
				path, i, d.Tag))
		}
	}
	if d.IndexTitle == "" {
		errs = append(errs, fmt.Errorf("%s: domain[%d] tag=%q: index_title is required", path, i, d.Tag))
	}
	if d.Blueprint == "" {
		errs = append(errs, fmt.Errorf("%s: domain[%d] tag=%q: blueprint is required", path, i, d.Tag))
	}
	if d.UnknownBucket != nil {
		for _, re := range bucketRejectREs {
			if re.MatchString(*d.UnknownBucket) {
				errs = append(errs, fmt.Errorf(
					"%s: domain[%d] tag=%q unknown_bucket=%q: contains forbidden pattern (security)",
					path, i, d.Tag, *d.UnknownBucket))
			}
		}
	}
	return errs
}

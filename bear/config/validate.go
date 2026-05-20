package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/barad1tos/noxctl/bear/fastpass"
)

// tagFormatRE allows lowercase / uppercase letters, digits, slash,
// underscore and hyphen. Anything else is rejected as a tag-injection
// surface (e.g. `tag = "foo;rm -rf /"`). The regex anchors to the
// whole string so partial matches don't slip through.
var tagFormatRE = regexp.MustCompile(`^[a-zA-Z0-9_/-]+$`)

// bucketRejectREs lists patterns that must NOT appear in user-
// supplied bucket names. Blocks bear://x-callback-url injection that
// would otherwise produce arbitrary callback wikilinks when a reader
// clicks the master.
var bucketRejectREs = []*regexp.Regexp{
	regexp.MustCompile(`://`),
	regexp.MustCompile(`(?i)^bear:`),
}

// supportedLocales locks v1 to Ukrainian. Future locales must add
// both their key here AND ship a `bear/locales/<key>.toml` catalog.
// Empty locale is allowed — caller treats as "use default".
var supportedLocales = map[string]struct{}{"uk": {}}

// ValidateCatalog runs catalog-level invariants AFTER successful TOML
// decode and BEFORE per-stanza dispatch. Aggregates every problem
// into one errors.Join return — never short-circuits on first
// failure.
//
// path is included in every nested error so multi-file callers
// (rare; the loader currently calls with one path) get
// disambiguation for free.
func ValidateCatalog(cat *Catalog, path string) error {
	var errs []error
	errs = append(errs, validateMeta(cat, path)...)
	errs = append(errs, validateDomains(cat, path)...)
	errs = append(errs, validatePromotions(cat, path)...)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// validatePromotions checks the [[promotion]] block for regressions the
// rules-driven promoter can't recover from at runtime:
//
//   - unknown `boundary` (typos silently produce a no-op rule),
//   - duplicate `from` (the index map keeps only one rule, the rest
//     of the operator's ladder is silently dropped),
//   - self-loop (`From == To`) — PromoteByCalendar's chain loop
//     advances `tag = next` and re-enters with the same rule, the
//     boundary check stays satisfied, and the function hangs,
//   - unreachable `To` — a typo'd target points at a tag no domain
//     owns and no other rule chains from, so promoted notes land
//     under a tag with no master/hub and silently disappear from the
//     regen pipeline.
//
// Aggregated like every other catalog-level check.
func validatePromotions(cat *Catalog, path string) []error {
	if len(cat.Promotions) == 0 {
		return nil
	}
	var errs []error
	seen := make(map[string]int, len(cat.Promotions))
	knownTargets := promotionTargetSet(cat)
	for i, p := range cat.Promotions {
		errs = append(errs, validatePromotionShape(p, i, path, knownTargets)...)
		if p.From == "" {
			continue
		}
		if prev, dup := seen[p.From]; dup {
			errs = append(errs, fmt.Errorf(
				"%s: promotion[%d] from=%q: duplicate; first declared at promotion[%d]",
				path, i, p.From, prev,
			))
		} else {
			seen[p.From] = i
		}
	}
	return errs
}

// validatePromotionShape collects every per-rule defect: missing
// from/to, unknown boundary, self-loop, unknown destination tag.
// Extracted from validatePromotions to keep the outer loop under the
// gocognit budget.
func validatePromotionShape(p Promotion, i int, path string, knownTargets map[string]struct{}) []error {
	var errs []error
	if p.From == "" {
		errs = append(errs, fmt.Errorf("%s: promotion[%d]: from is required", path, i))
		// from-less rule can't meaningfully report the rest with a
		// useful identifier — return early so error stream stays
		// readable.
		return errs
	}
	if p.To == "" {
		errs = append(errs, fmt.Errorf("%s: promotion[%d] from=%q: to is required", path, i, p.From))
	}
	if _, ok := fastpass.ValidPromotionBoundaries[p.Boundary]; !ok {
		errs = append(errs, fmt.Errorf(
			"%s: promotion[%d] from=%q: unknown boundary %q (valid: day|week|month|year, empty = day)",
			path, i, p.From, p.Boundary,
		))
	}
	if p.To != "" && p.From == p.To {
		errs = append(errs, fmt.Errorf(
			"%s: promotion[%d] from=%q: self-loop (from == to); time-promotion would advance forever",
			path, i, p.From,
		))
	}
	if p.To != "" && p.From != p.To {
		if _, ok := knownTargets[p.To]; !ok {
			errs = append(errs, fmt.Errorf(
				"%s: promotion[%d] from=%q: to=%q does not match any declared domain tag or another promotion's from",
				path, i, p.From, p.To,
			))
		}
	}
	return errs
}

// promotionTargetSet returns the universe of tags a `[[promotion]]
// .to` field may legitimately reference: every declared domain Tag
// plus every promotion's From (chained ladders are valid even if the
// intermediate hop isn't a top-level domain — the engine simply
// keeps rewriting the canonical tag-line). Used by validation to
// catch typos in `to` at load time, not at first sweep.
func promotionTargetSet(cat *Catalog) map[string]struct{} {
	out := make(map[string]struct{}, len(cat.Domains)+len(cat.Promotions))
	for _, d := range cat.Domains {
		if d.Tag != "" {
			out[d.Tag] = struct{}{}
		}
	}
	for _, p := range cat.Promotions {
		if p.From != "" {
			out[p.From] = struct{}{}
		}
	}
	return out
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
				path, cat.Meta.Locale,
			))
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
				path, i, d.Tag,
			))
		}
		if strings.Count(d.Tag, "/") > 1 {
			errs = append(errs, fmt.Errorf(
				"%s: domain[%d] tag=%q: tag tree depth limit exceeded (max 2 segments)",
				path, i, d.Tag,
			))
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
					path, i, d.Tag, *d.UnknownBucket,
				))
			}
		}
	}
	return errs
}
